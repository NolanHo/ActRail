import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AppProviders } from "../../app/providers";
import { api } from "../../lib/api";
import { SessionsPane } from "./SessionsPane";

vi.mock("../../lib/api", () => ({
  api: {
    createSession: vi.fn().mockResolvedValue({ ok: true, session_id: "sess-2", broker_pid: 42 }),
    handoffSession: vi.fn().mockResolvedValue({ ok: true, session_id: "sess-2", runtime_id: "rt-2", broker_pid: 42 }),
    restartSession: vi.fn().mockResolvedValue({ ok: true, session_id: "sess-1", runtime_id: "rt-2", previous_runtime_id: "rt-1", broker_pid: 42 }),
    editSession: vi.fn().mockResolvedValue({ ok: true, alias: "Updated session" }),
    deleteSession: vi.fn().mockResolvedValue({ ok: true }),
    setSessionFocus: vi.fn().mockResolvedValue({ ok: true, focused: true }),
    getSessionDetails: vi.fn().mockResolvedValue({ ok: true, session: { session_id: "sess-1", alias: "Inbox cleanup", agent_backend: "pi", priority_offset: 0 } }),
  },
}));

let root: HTMLDivElement | null = null;

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

async function click(element: Element) {
  await act(async () => {
    (element as HTMLElement).dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

async function openSessionMenu() {
  const menuButton = root?.querySelector<HTMLButtonElement>('button[aria-label="More session actions"]');
  expect(menuButton).not.toBeNull();
  await click(menuButton!);
  await flush();
}

function createSessionsStore(initialState: any, options?: { onRefresh?: () => void | Promise<void> }) {
  let state = initialState;
  const listeners = new Set<() => void>();

  const emit = () => listeners.forEach((listener) => listener());

  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    refresh: vi.fn(async (_options?: { preferNewest?: boolean }) => {
      await options?.onRefresh?.();
      emit();
    }),
    refreshBootstrap: vi.fn(async () => {
      await options?.onRefresh?.();
      emit();
    }),
    loadMore: vi.fn(async () => {
      await options?.onRefresh?.();
      emit();
    }),
    select: vi.fn((sessionId: string) => {
      state = { ...state, activeSessionId: sessionId };
      emit();
    }),
    setState(next: any) {
      state = next;
      emit();
    },
  };
}

function renderSessionsPane(
  state: any,
  options?: {
    onRefresh?: () => void | Promise<void>;
    composerStore?: any;
    initialTab?: "focus" | "sessions";
  },
) {
  const sessionsStore = createSessionsStore(state, options);
  const composerStore = options?.composerStore ?? {
    getState: () => ({ draftBySessionId: {}, sending: false, pendingBySessionId: {} }),
    subscribe: () => () => undefined,
    setDraft: vi.fn(),
    copyDraft: vi.fn(),
    submit: vi.fn(),
    clearAcknowledgedPending: vi.fn(),
  };
  root = document.createElement("div");
  document.body.appendChild(root);
  render(
    <AppProviders sessionsStore={sessionsStore as any} composerStore={composerStore as any}>
      <SessionsPane />
    </AppProviders>,
    root,
  );
  if ((options?.initialTab ?? "sessions") === "sessions") {
    const sessionsTab = Array.from(root.querySelectorAll<HTMLButtonElement>("button")).find((button) => button.textContent?.trim() === "Sessions");
    if (sessionsTab) {
      act(() => {
        sessionsTab.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
    }
  }
  return Object.assign(sessionsStore, { composerStore });
}

describe("SessionsPane", () => {
  afterEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
  });

  it("defaults to Focus tab and renders Focus before Sessions", () => {
    renderSessionsPane({
      items: [
        { session_id: "sess-1", alias: "Inbox cleanup", agent_backend: "pi", focused: true },
        { session_id: "sess-2", alias: "Release prep", agent_backend: "pi", focused: false },
      ],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    }, { initialTab: "focus" });

    const tabButtons = Array.from(root?.querySelectorAll<HTMLButtonElement>(".sessionsSurfaceTabs button") || []);
    expect(tabButtons[0]?.textContent).toContain("Focus");
    expect(tabButtons[1]?.textContent).toContain("Sessions");
    expect(root?.textContent).toContain("Inbox cleanup");
    expect(root?.textContent).not.toContain("Release prep");
  });

  it("renders active session badges", () => {
    renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Inbox cleanup", agent_backend: "pi", busy: true, owned: true, queue_len: 2 }],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    expect(root?.querySelector("[data-testid='sessions-surface']")).not.toBeNull();
    expect(root?.querySelectorAll("[data-testid='session-card']")).toHaveLength(1);
    expect(root?.querySelector("[data-testid='session-card'][aria-current='true']")).not.toBeNull();
    expect(root?.textContent).toContain("Inbox cleanup");
    expect(root?.textContent).toContain("pi");
    expect(root?.textContent).not.toContain("web");
  });

  it("uses first user message as title and hides cwd in compact row", () => {
    renderSessionsPane({
      items: [{ session_id: "sess-1", first_user_message: "我准备用 preact + vite 重构web端，请帮我出个规划", cwd: "/Users/huapeixuan/Documents/Code/codoxear", agent_backend: "pi" }],
      activeSessionId: null,
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    expect(root?.querySelector(".sessionTitle")?.textContent).toContain("我准备用 preact + vite 重构web端，请帮我出个规划");
    expect(root?.textContent).not.toContain("/Users/huapeixuan/Documents/Code/codoxear");
  });

  it("prefers persisted title over first user message when alias is missing", () => {
    renderSessionsPane({
      items: [{ session_id: "sess-1", title: "Release checklist", first_user_message: "先整理一下今晚要发的内容", agent_backend: "pi" }],
      activeSessionId: null,
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    expect(root?.querySelector(".sessionTitle")?.textContent).toContain("Release checklist");
    expect(root?.querySelector(".sessionTitle")?.textContent).not.toContain("先整理一下今晚要发的内容");
  });

  it("switches between Sessions and Focus tabs", async () => {
    const sessionsStore = renderSessionsPane({
      items: [
        { session_id: "sess-1", alias: "Inbox cleanup", agent_backend: "pi", focused: true, cwd: "/tmp/a" },
        { session_id: "sess-2", alias: "Release prep", agent_backend: "pi", focused: false, cwd: "/tmp/b" },
      ],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    expect(root?.textContent).toContain("Inbox cleanup");
    expect(root?.textContent).toContain("Release prep");

    const focusTab = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Focus"));
    expect(focusTab).toBeDefined();
    await click(focusTab!);
    await flush();

    expect(root?.textContent).toContain("Inbox cleanup");
    expect(root?.textContent).not.toContain("Release prep");
    expect(sessionsStore.refresh).toHaveBeenCalled();
  });

  it("toggles Focus from the session rail", async () => {
    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Inbox cleanup", agent_backend: "pi", focused: false }],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    const focusButton = root?.querySelector<HTMLButtonElement>('button[aria-label="Add to Focus"]');
    expect(focusButton).not.toBeNull();
    await click(focusButton!);
    await flush();

    expect(api.setSessionFocus).toHaveBeenCalledWith("sess-1", true, null);
    expect(sessionsStore.refresh).toHaveBeenCalled();
  });

  it("keeps focused historical sessions in Focus and allows removing Focus", async () => {
    const sessionsStore = renderSessionsPane({
      items: [
        { session_id: "history:pi:resume-1", alias: "Recovered planning", agent_backend: "pi", historical: true, focused: true },
        { session_id: "sess-2", alias: "Release prep", agent_backend: "pi", focused: false },
      ],
      activeSessionId: "history:pi:resume-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    const focusTab = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Focus"));
    expect(focusTab).toBeDefined();
    await click(focusTab!);
    await flush();

    expect(root?.textContent).toContain("Recovered planning");
    expect(root?.textContent).not.toContain("Release prep");

    const unfocusButton = root?.querySelector<HTMLButtonElement>('button[aria-label="Remove from Focus"]');
    expect(unfocusButton).not.toBeNull();
    await click(unfocusButton!);
    await flush();

    expect(api.setSessionFocus).toHaveBeenCalledWith("history:pi:resume-1", false, null);
    expect(sessionsStore.refresh).toHaveBeenCalled();
  });

  it("deletes a historical session after dialog confirmation", async () => {
    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "history:pi:resume-1", alias: "Recovered", agent_backend: "pi", historical: true }],
      activeSessionId: "history:pi:resume-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    await openSessionMenu();
    const deleteAction = Array.from(root?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') || []).find((button) => button.textContent?.includes("Delete"));
    expect(deleteAction).toBeDefined();
    await click(deleteAction!);
    await flush();

    const confirmButton = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Delete session"));
    expect(confirmButton).toBeDefined();
    await click(confirmButton!);
    await flush();

    expect(api.deleteSession).toHaveBeenCalledWith("history:pi:resume-1");
    expect(sessionsStore.refresh).toHaveBeenCalled();
  });

  it("deletes a session after dialog confirmation", async () => {
    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Inbox cleanup", agent_backend: "pi" }],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    await openSessionMenu();
    const deleteAction = Array.from(root?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') || []).find((button) => button.textContent?.includes("Delete"));
    expect(deleteAction).toBeDefined();
    await click(deleteAction!);
    await flush();

    const confirmButton = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Delete session"));
    expect(confirmButton).toBeDefined();
    await click(confirmButton!);
    await flush();

    expect(api.deleteSession).toHaveBeenCalledWith("sess-1");
    expect(sessionsStore.refresh).toHaveBeenCalled();
  });

  it("does not show handoff for a pending pi session", async () => {
    renderSessionsPane({
      items: [{ session_id: "sess-pending", alias: "Pending", agent_backend: "pi", pending_startup: true }],
      activeSessionId: "sess-pending",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: true,
    });

    await openSessionMenu();
    expect(Array.from(root?.querySelectorAll('[role="menuitem"]') || []).some((node) => (node.textContent || "").includes("Handoff"))).toBe(false);
  });

  it("restarts a pi session from the dialog and keeps the same durable session selected", async () => {
    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Inbox cleanup", cwd: "/tmp/project", agent_backend: "pi", runtime_id: "rt-1" }],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: ["/tmp/project"],
      cwdGroups: {},
      tmuxAvailable: true,
    });

    sessionsStore.refresh = vi.fn(async () => {
      sessionsStore.setState({
        ...sessionsStore.getState(),
        items: [{ session_id: "sess-1", alias: "Inbox cleanup", cwd: "/tmp/project", agent_backend: "pi", runtime_id: "rt-2" }],
      });
    });

    await openSessionMenu();
    const restartAction = Array.from(root?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') || []).find((button) => button.textContent?.includes("Restart Pi"));
    expect(restartAction).toBeDefined();
    await click(restartAction!);
    await flush();

    const confirmButton = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Restart Pi"));
    expect(confirmButton).toBeDefined();
    await click(confirmButton!);
    await flush();

    expect(api.restartSession).toHaveBeenCalledWith("sess-1", "rt-1");
    expect(sessionsStore.select).toHaveBeenCalledWith("sess-1");
  });

  it("hands off a pi session and preserves the draft under the new session id", async () => {
    const composerStore = {
      getState: () => ({ draftBySessionId: { "sess-1": "keep draft" }, sending: false, pendingBySessionId: {} }),
      subscribe: () => () => undefined,
      setDraft: vi.fn(),
      copyDraft: vi.fn(),
      submit: vi.fn(),
      clearAcknowledgedPending: vi.fn(),
    };
    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Inbox cleanup", cwd: "/tmp/project", agent_backend: "pi", runtime_id: "rt-1" }],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: ["/tmp/project"],
      cwdGroups: {},
      tmuxAvailable: true,
    }, {
      composerStore,
    });

    sessionsStore.refresh = vi.fn(async () => {
      sessionsStore.setState({
        ...sessionsStore.getState(),
        items: [{ session_id: "sess-2", alias: "Inbox cleanup", cwd: "/tmp/project", agent_backend: "pi", runtime_id: "rt-2" }],
      });
    });

    await openSessionMenu();
    const handoffAction = Array.from(root?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') || []).find((button) => button.textContent?.includes("Handoff"));
    expect(handoffAction).toBeDefined();
    await click(handoffAction!);
    await flush();

    const confirmButton = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Handoff session"));
    expect(confirmButton).toBeDefined();
    await click(confirmButton!);
    await flush();

    expect(api.handoffSession).toHaveBeenCalledWith("sess-1", "rt-1");
    expect(composerStore.copyDraft).toHaveBeenCalledWith("sess-1", "sess-2");
    expect(sessionsStore.select).toHaveBeenCalledWith("sess-2");
  });

  it("prefers the returned durable handoff session id over runtime id", async () => {
    vi.mocked(api.handoffSession).mockResolvedValue({
      ok: true,
      session_id: "dur-new",
      runtime_id: "rt-new",
      broker_pid: 333,
      backend: "pi",
    } as any);

    const composerStore = {
      getState: () => ({ draftBySessionId: { "sess-1": "keep draft" }, sending: false, pendingBySessionId: {} }),
      subscribe: () => () => undefined,
      setDraft: vi.fn(),
      copyDraft: vi.fn(),
      submit: vi.fn(),
      clearAcknowledgedPending: vi.fn(),
    };
    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Inbox cleanup", cwd: "/tmp/project", agent_backend: "pi", runtime_id: "rt-1" }],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: ["/tmp/project"],
      cwdGroups: {},
      tmuxAvailable: true,
    }, {
      composerStore,
    });

    sessionsStore.refresh = vi.fn(async () => {
      sessionsStore.setState({
        ...sessionsStore.getState(),
        items: [
          { session_id: "dur-new", alias: "Inbox cleanup", cwd: "/tmp/project", agent_backend: "pi", runtime_id: "rt-new" },
          { session_id: "rt-new", alias: "stale runtime row", cwd: "/tmp/project", agent_backend: "pi", runtime_id: "rt-old-shadow" },
        ],
      });
    });

    await openSessionMenu();
    const handoffAction = Array.from(root?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') || []).find((button) => button.textContent?.includes("Handoff"));
    expect(handoffAction).toBeDefined();
    await click(handoffAction!);
    await flush();

    const confirmButton = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => button.textContent?.includes("Handoff session"));
    expect(confirmButton).toBeDefined();
    await click(confirmButton!);
    await flush();

    expect(composerStore.copyDraft).toHaveBeenCalledWith("sess-1", "dur-new");
    expect(sessionsStore.select).toHaveBeenCalledWith("dur-new");
    expect(sessionsStore.select).not.toHaveBeenCalledWith("rt-new");
  });

  it("duplicates a session from details and selects returned session", async () => {
    vi.mocked(api.getSessionDetails).mockResolvedValue({
      ok: true,
      session: {
        session_id: "sess-1",
        cwd: "/tmp/project",
        agent_backend: "codex",
        provider_choice: "openai-api",
        model: "gpt-5.4",
        reasoning_effort: "high",
        service_tier: "fast",
        transport: "tmux",
      },
    } as any);

    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Inbox cleanup", cwd: "/tmp/project", agent_backend: "codex" }],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: ["/tmp/project"],
      cwdGroups: {},
      tmuxAvailable: true,
    });

    sessionsStore.refresh = vi.fn(async () => {
      sessionsStore.setState({
        ...sessionsStore.getState(),
        items: [...sessionsStore.getState().items, { session_id: "sess-2", alias: "Inbox cleanup copy", cwd: "/tmp/project", agent_backend: "codex" }],
      });
    });

    await openSessionMenu();
    const duplicateAction = Array.from(root?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') || []).find((button) => button.textContent?.includes("Duplicate"));
    expect(duplicateAction).toBeDefined();
    await click(duplicateAction!);
    await flush();

    expect(api.getSessionDetails).toHaveBeenCalledWith("sess-1");
    expect(api.createSession).toHaveBeenCalledWith({
      cwd: "/tmp/project",
      backend: "codex",
      model: "gpt-5.4",
      model_provider: "openai",
      preferred_auth_method: "apikey",
      reasoning_effort: "high",
      service_tier: "fast",
      create_in_tmux: true,
    });
    expect(sessionsStore.select).toHaveBeenCalledWith("sess-2");
  });

  it("selects historical pi sessions without resuming them immediately", async () => {
    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "history:pi:resume-hist", alias: "Recovered planning thread", cwd: "/tmp/project", agent_backend: "pi", historical: true }],
      activeSessionId: null,
      loading: false,
      newSessionDefaults: null,
      recentCwds: ["/tmp/project"],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    const sessionButton = root?.querySelector<HTMLButtonElement>(".sessionCardButton");
    expect(sessionButton).not.toBeNull();
    await click(sessionButton!);
    await flush();

    expect(api.createSession).not.toHaveBeenCalled();
    expect(sessionsStore.select).toHaveBeenCalledWith("history:pi:resume-hist");
  });

  it("opens edit dialog from icon action and saves fields", async () => {
    vi.mocked(api.getSessionDetails).mockResolvedValue({
      ok: true,
      session: { session_id: "sess-1", alias: "Inbox cleanup", first_user_message: "整理一下今天的会话", agent_backend: "pi", priority_offset: 0 },
    } as any);

    const sessionsStore = renderSessionsPane({
      items: [
        { session_id: "sess-1", alias: "Inbox cleanup", first_user_message: "整理一下今天的会话", agent_backend: "pi" },
        { session_id: "sess-2", alias: "Release prep", agent_backend: "pi" },
      ],
      activeSessionId: "sess-1",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    const editButton = root?.querySelector<HTMLButtonElement>('button[aria-label="Edit session"]');
    expect(editButton).not.toBeNull();
    await click(editButton!);
    await flush();

    const dependencySelect = root?.querySelector('select[name="dependencySessionId"]') as HTMLSelectElement;
    await act(async () => {
      dependencySelect.value = "sess-2";
      dependencySelect.dispatchEvent(new Event("change", { bubbles: true }));
    });

    const saveButton = Array.from(root?.querySelectorAll("button") || []).find((button) => button.textContent?.includes("Save changes"));
    expect(saveButton).toBeDefined();
    saveButton?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    await flush();

    expect(api.editSession).toHaveBeenCalledWith("sess-1", {
      name: "Inbox cleanup",
      priority_offset: 0,
      snooze_until: null,
      dependency_session_id: "sess-2",
    });
    expect(sessionsStore.refresh).toHaveBeenCalled();
  });

  it("renders a flat session list without cwd grouping", () => {
    renderSessionsPane({
      items: [
        { session_id: "sess-1", alias: "Docs polish", cwd: "/work/docs", agent_backend: "pi", updated_ts: 30 },
        { session_id: "sess-2", alias: "Bug bash", cwd: "/work/api", agent_backend: "codex", updated_ts: 120 },
        { session_id: "sess-3", alias: "Release notes", cwd: "/work/docs", agent_backend: "pi", updated_ts: 20 },
      ],
      activeSessionId: "sess-3",
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    expect(root?.querySelectorAll<HTMLElement>(".sessionGroup")).toHaveLength(0);
    expect(Array.from(root?.querySelectorAll<HTMLElement>(".sessionTitle") || []).map((node) => node.textContent?.trim())).toEqual([
      "Docs polish",
      "Bug bash",
      "Release notes",
    ]);
  });

  it("selects grouped session on card click", async () => {
    const sessionsStore = renderSessionsPane({
      items: [
        { session_id: "sess-1", alias: "Docs polish", cwd: "/work/docs", agent_backend: "pi" },
        { session_id: "sess-2", alias: "Release notes", cwd: "/work/docs", agent_backend: "pi" },
      ],
      activeSessionId: null,
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    const cardButtons = root?.querySelectorAll<HTMLButtonElement>(".sessionCardButton") || [];
    await click(cardButtons[1]!);
    await flush();

    expect(sessionsStore.select).toHaveBeenCalledWith("sess-2");
  });

  it("loads more sessions with a single flat-list control", async () => {
    const sessionsStore = renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Session 1", cwd: "/work/docs", agent_backend: "pi" }],
      activeSessionId: null,
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
      remainingCount: 3,
    });

    const loadMore = Array.from(root?.querySelectorAll<HTMLButtonElement>("button") || []).find((button) => (button.textContent || "").includes("Load 3 more sessions"));
    expect(loadMore).toBeDefined();

    await click(loadMore!);
    await flush();

    expect(sessionsStore.loadMore).toHaveBeenCalledTimes(1);
  });

  it("does not render cwd group controls in the flat rail", () => {
    renderSessionsPane({
      items: [{ session_id: "sess-1", alias: "Inbox", agent_backend: "pi" }],
      activeSessionId: null,
      loading: false,
      newSessionDefaults: null,
      recentCwds: [],
      cwdGroups: {},
      tmuxAvailable: false,
    });

    expect(root?.querySelector(".sessionGroupTitle")).toBeNull();
    expect(root?.querySelector(".sessionGroupRenameButton")).toBeNull();
  });
});
