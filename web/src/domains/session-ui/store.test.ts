import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import { createSessionUiStore } from "./store";

vi.mock("../../lib/api", () => ({
  api: {
    getWorkspace: vi.fn(),
  },
}));

describe("createSessionUiStore", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  function createDeferred<T>() {
    let resolve!: (value: T | PromiseLike<T>) => void;
    let reject!: (reason?: unknown) => void;
    const promise = new Promise<T>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  }

  it("refreshes workspace panels from the backend workspace snapshot", async () => {
    vi.mocked(api.getWorkspace).mockResolvedValue({
      root_path: "/tmp/project",
      selected_path: "src/main.tsx",
      open_paths: ["src/main.tsx"],
      history_items: [{ path: "README.md", label: "README" }],
    } as any);

    const store = createSessionUiStore();
    await store.refresh("s1");

    expect(api.getWorkspace).toHaveBeenCalledWith("s1");
    expect(store.getState()).toEqual({
      sessionId: "s1",
      runtimeId: null,
      diagnostics: {
        root_path: "/tmp/project",
        selected_path: "src/main.tsx",
        history_items: [{ path: "README.md", label: "README" }],
      },
      queue: null,
      files: ["src/main.tsx", "README.md"],
      loading: false,
    });
  });

  it("refreshes workspace data the same way for non-pi sessions", async () => {
    vi.mocked(api.getWorkspace).mockResolvedValue({ root_path: "/tmp/project" } as any);

    const store = createSessionUiStore();
    await store.refresh("s1", { agentBackend: "codex" });

    expect(api.getWorkspace).toHaveBeenCalledWith("s1");
    expect(store.getState().diagnostics).toEqual({ root_path: "/tmp/project" });
  });

  it("keeps same-session workspace data visible while a refresh is in flight", async () => {
    vi.mocked(api.getWorkspace).mockResolvedValueOnce({
      root_path: "/tmp/project-a",
      open_paths: ["src/a.ts"],
    } as any);

    const nextWorkspace = createDeferred<Record<string, unknown>>();

    vi.mocked(api.getWorkspace).mockReturnValueOnce(nextWorkspace.promise as any);

    const store = createSessionUiStore();

    await store.refresh("s1");

    const refreshPromise = store.refresh("s1");

    expect(store.getState()).toEqual({
      sessionId: "s1",
      runtimeId: null,
      diagnostics: { root_path: "/tmp/project-a" },
      queue: null,
      files: ["src/a.ts"],
      loading: true,
    });

    nextWorkspace.resolve({
      root_path: "/tmp/project-b",
      open_paths: ["src/b.ts"],
    });
    await refreshPromise;

    expect(store.getState()).toEqual({
      sessionId: "s1",
      runtimeId: null,
      diagnostics: { root_path: "/tmp/project-b" },
      queue: null,
      files: ["src/b.ts"],
      loading: false,
    });
  });

  it("clears workspace data immediately when switching sessions", async () => {
    vi.mocked(api.getWorkspace).mockResolvedValueOnce({
      root_path: "/tmp/project-a",
      open_paths: ["src/a.ts"],
    } as any);

    const nextWorkspace = createDeferred<Record<string, unknown>>();

    vi.mocked(api.getWorkspace).mockReturnValueOnce(nextWorkspace.promise as any);

    const store = createSessionUiStore();

    await store.refresh("s1");

    const refreshPromise = store.refresh("s2");

    expect(store.getState()).toEqual({
      sessionId: "s2",
      runtimeId: null,
      diagnostics: null,
      queue: null,
      files: [],
      loading: true,
    });

    nextWorkspace.resolve({
      root_path: "/tmp/project-b",
      open_paths: ["src/b.ts"],
    });
    await refreshPromise;

    expect(store.getState()).toEqual({
      sessionId: "s2",
      runtimeId: null,
      diagnostics: { root_path: "/tmp/project-b" },
      queue: null,
      files: ["src/b.ts"],
      loading: false,
    });
  });

  it("reuses an in-flight refresh for the same session and runtime", async () => {
    const deferred = createDeferred<Record<string, unknown>>();
    vi.mocked(api.getWorkspace).mockReturnValueOnce(deferred.promise as any);

    const store = createSessionUiStore();
    const first = store.refresh("s1", { runtimeId: "rt-1" });
    const second = store.refresh("s1", { runtimeId: "rt-1" });

    expect(api.getWorkspace).toHaveBeenCalledTimes(1);

    deferred.resolve({
      runtime_id: "rt-1",
      root_path: "/tmp/project",
      open_paths: ["src/main.tsx"],
    });
    await Promise.all([first, second]);

    expect(store.getState()).toEqual({
      sessionId: "s1",
      runtimeId: "rt-1",
      diagnostics: { root_path: "/tmp/project" },
      queue: null,
      files: ["src/main.tsx"],
      loading: false,
    });
  });
});
