import { render } from "preact";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../app/providers";
import { SessionWorkspace } from "./SessionWorkspace";

vi.mock("../../lib/api", () => ({
  api: {
    listMessages: vi.fn().mockResolvedValue({ items: [], tail_seq: 0 }),
    submitUiResponse: vi.fn().mockResolvedValue({ ok: true }),
  },
}));

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

function createStaticStore<TState extends object, TActions extends Record<string, (...args: any[]) => any>>(
  state: TState,
  actions: TActions,
) {
  return {
    getState: () => state,
    subscribe: () => () => undefined,
    ...actions,
  };
}

describe("SessionWorkspace", () => {
  let root: HTMLDivElement | null = null;

  afterEach(() => {
    vi.clearAllMocks();
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
  });

  it("renders metadata and diagnostics in details mode", () => {
    const sessionUiStore = createStaticStore(
      {
        sessionId: "sess-9",
        runtimeId: "runtime-9",
        diagnostics: { status: "ok", queue_len: 2 },
        queue: { items: [{ text: "next task" }] },
        files: ["src/main.tsx"],
        loading: false,
        requests: [],
      },
      { refresh: vi.fn() },
    );
    const sessionsStore = createStaticStore(
      {
        items: [
          {
            session_id: "sess-9",
            runtime_id: "runtime-9",
            alias: "Workflow",
            cwd: "/work/docs",
            session_file_path: "/tmp/actrail-session.jsonl",
            backend_session_id: "pi-backend-1",
            git_branch: "main",
            agent_backend: "pi",
            transport: "pi-rpc",
            busy: false,
            focused: true,
            queue_len: 2,
          },
        ],
        activeSessionId: "sess-9",
        loading: false,
      },
      { refresh: vi.fn(), refreshBootstrap: vi.fn(), loadMoreGroup: vi.fn(), loadMoreGroups: vi.fn(), select: vi.fn() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionUiStore={sessionUiStore as any} sessionsStore={sessionsStore as any}>
        <SessionWorkspace mode="details" />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Metadata");
    expect(root.textContent).toContain("Workflow");
    expect(root.textContent).toContain("runtime-9");
    expect(root.textContent).toContain("/work/docs");
    expect(root.textContent).toContain("Session file");
    expect(root.textContent).toContain("/tmp/actrail-session.jsonl");
    expect(root.textContent).toContain("Backend Session Id");
    expect(root.textContent).toContain("pi-backend-1");
    expect(root.textContent).toContain("Diagnostics");
    expect(root.textContent).toContain("Status");
    expect(root.textContent).toContain("Queue");
    expect(root.textContent).toContain("next task");
    expect(root.textContent).toContain("src/main.tsx");
  });

  it("calculates context usage only after manual action", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.listMessages).mockResolvedValueOnce({
      items: [
        { role: "system", text: "Follow instructions." },
        { role: "user", text: "Read file" },
        { type: "tool", name: "read", text: "file content" },
        { role: "assistant", text: "File read." },
      ],
      tail_seq: 4,
     } as never);
    const sessionUiStore = createStaticStore(
      {
        sessionId: "sess-usage",
        runtimeId: "runtime-usage",
        diagnostics: null,
        queue: null,
        files: [],
        loading: false,
        requests: [],
      },
      { refresh: vi.fn() },
    );
    const sessionsStore = createStaticStore(
      {
        items: [{ session_id: "sess-usage", runtime_id: "runtime-usage", model: "unknown-model", agent_backend: "pi" }],
        activeSessionId: "sess-usage",
        loading: false,
      },
      { refresh: vi.fn(), refreshBootstrap: vi.fn(), loadMoreGroup: vi.fn(), loadMoreGroups: vi.fn(), select: vi.fn() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionUiStore={sessionUiStore as any} sessionsStore={sessionsStore as any}>
        <SessionWorkspace mode="details" initialTab="metadata" />
      </AppProviders>,
      root,
    );

    expect(api.listMessages).not.toHaveBeenCalled();
    expect(root.textContent).toContain("Manual calculation is required.");

    const button = Array.from(root.querySelectorAll("button")).find((item) => item.textContent === "Calculate") as HTMLButtonElement;
    button.click();
    await flush();
    await flush();
    await flush();

    expect(api.listMessages).toHaveBeenCalledWith("sess-usage", true, undefined, undefined, undefined, undefined, "runtime-usage");
    await vi.waitFor(() => expect(root?.textContent).toContain("system prompt:"));
    expect(root.textContent).toContain("tool:");
    expect(root.textContent).toContain("user:");
    expect(root.textContent).toContain("assist:");
    expect(root.textContent).toContain("fallback: chars/4");
  });

  it("renders Pi details with session-file rows without the todo snapshot view", () => {
    const sessionUiStore = createStaticStore(
      {
        sessionId: "pi-details",
        diagnostics: {
          log_path: "/tmp/pi-broker.log",
          session_file_path: "/tmp/pi-session.jsonl",
          updated_ts: 1_700_000_100,
          todo_snapshot: {
            available: true,
            error: false,
            progress_text: "1/2 completed",
            items: [
              {
                id: 1,
                title: "Explore project context",
                description: "Inspect the current web app",
                status: "completed",
              },
              {
                id: 2,
                title: "Restore history controls",
                status: "in-progress",
              },
            ],
          },
        },
        queue: null,
        files: [],
        loading: false,
        requests: [],
      },
      { refresh: vi.fn().mockResolvedValue(undefined) },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionUiStore={sessionUiStore as any}>
        <SessionWorkspace mode="details" />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Session file");
    expect(root.textContent).toContain("/tmp/pi-session.jsonl");
    expect(root.textContent).toContain("Log");
    expect(root.textContent).toContain("/tmp/pi-broker.log");
    expect(root.textContent).toContain("Context Usage");
    expect(root.textContent).not.toContain("Todo list");
    expect(root.textContent).not.toContain("1/2 completed");
    expect(root.textContent).not.toContain("Explore project context");
    expect(root.textContent).not.toContain("Restore history controls");
    expect(root.textContent).not.toContain("session_file_path");
    expect(root.textContent).not.toContain("todo_snapshot");
  });
});
