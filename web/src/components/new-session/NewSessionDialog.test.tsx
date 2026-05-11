import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../app/providers";
import { NewSessionDialog } from "./NewSessionDialog";

vi.mock("../../lib/api", () => ({
  api: {
    createSession: vi.fn(),
    getSessionResumeCandidates: vi.fn(),
    renameSession: vi.fn(),
  },
}));

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

async function wait(ms: number) {
  await act(async () => {
    await new Promise((resolve) => window.setTimeout(resolve, ms));
  });
}

async function setInputValue(element: HTMLInputElement, value: string) {
  await act(async () => {
    element.value = value;
    element.dispatchEvent(new Event("input", { bubbles: true }));
    element.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

async function setSelectValue(element: HTMLSelectElement, value: string) {
  await act(async () => {
    element.value = value;
    element.dispatchEvent(new Event("input", { bubbles: true }));
    element.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

async function setCheckboxValue(element: HTMLInputElement, checked: boolean) {
  await act(async () => {
    element.checked = checked;
    element.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

async function submitForm(element: HTMLFormElement) {
  await act(async () => {
    if (typeof element.requestSubmit === "function") {
      element.requestSubmit();
    } else {
      element.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    }
  });
}

function createSessionsStore(initialState: any) {
  let state = initialState;
  const listeners = new Set<() => void>();
  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    refresh: vi.fn(async (options?: { preferNewest?: boolean }) => {
      if (options?.preferNewest) {
        state = {
          ...state,
          activeSessionId: state.items[0]?.session_id ?? null,
        };
      }
      listeners.forEach((listener) => listener());
    }),
    refreshBootstrap: vi.fn(async (_options?: { refreshPiModels?: boolean }) => {
      state = { ...state, bootstrapLoaded: true };
      listeners.forEach((listener) => listener());
    }),
    select: vi.fn((sessionId: string) => {
      state = { ...state, activeSessionId: sessionId };
      listeners.forEach((listener) => listener());
    }),
    upsertSession: vi.fn((session: any, options?: { prepend?: boolean; select?: boolean }) => {
      const withoutExisting = (state.items ?? []).filter((item: any) => item.session_id !== session.session_id);
      state = {
        ...state,
        items: options?.prepend === false ? [...withoutExisting, session] : [session, ...withoutExisting],
        activeSessionId: options?.select ? session.session_id : state.activeSessionId,
      };
      listeners.forEach((listener) => listener());
    }),
    setState(next: any) {
      state = next;
      listeners.forEach((listener) => listener());
    },
  };
}

describe("NewSessionDialog", () => {
  let root: HTMLDivElement | null = null;

  afterEach(() => {
    vi.clearAllMocks();
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
  });

  it("defaults the cwd field to the active session cwd when opening the dialog", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [
        { session_id: "sess-1", cwd: "/tmp/other", agent_backend: "pi" },
      ],
      activeSessionId: "sess-1",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/recent"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: {},
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const cwdInput = root.querySelector('input[placeholder="/path/to/project"]') as HTMLInputElement;
    expect(cwdInput.value).toBe("/tmp/other");
  });

  it("does not default launch controls from the active session when bootstrap defaults are empty", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [{ session_id: "active-pi", cwd: "/root/docs", agent_backend: "pi", provider_choice: "openai", model: "gpt-5.5", reasoning_effort: "high" }],
      activeSessionId: "active-pi",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/root/docs"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: {},
          codex: {},
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    expect((root.querySelector('select[name="providerChoice"]') as HTMLSelectElement).value).toBe("");
    expect((root.querySelector('input[name="model"]') as HTMLInputElement).value).toBe("");
    expect((root.querySelector('select[name="reasoningEffort"]') as HTMLSelectElement).value).toBe("high");
  });

  it("lets default Codex launches inherit provider and model from Codex config", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({ session_id: "new", backend: "codex", ok: true } as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/root/docs"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: { provider_choice: "crs", provider_choices: ["crs"], model: "gpt-5.5", models: ["gpt-5.5"] },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();

    expect(api.createSession).toHaveBeenCalledWith(expect.objectContaining({
      agent_backend: "codex",
      provider: undefined,
      model: undefined,
      reasoning_effort: undefined,
    }));
  });

  it("does not load or select backend history while Start is active", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [{ session_id: "history:pi:old", title: "Old history" }],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/root/docs"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: { provider_choice: "openai", model: "gpt-5.4" },
          codex: {},
        },
        backend_capabilities: {
          pi: { resume_history: true },
          codex: { resume_history: false },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await wait(220);
    await flush();

    expect(api.getSessionResumeCandidates).not.toHaveBeenCalled();
    expect(root.textContent).not.toContain("Old history");
  });

  it("shows resume history for codex when the backend supports it", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [{ session_id: "history:codex:old", title: "Codex history" }],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/root/docs"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: {},
          pi: {},
        },
        backend_capabilities: {
          codex: { resume_history: true },
          pi: { resume_history: true },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await wait(220);
    await flush();

    const resumeTab = Array.from(root.querySelectorAll("button")).find((node) => node.textContent?.trim() === "Resume") as HTMLButtonElement;
    expect(resumeTab).toBeDefined();
    await act(async () => {
      resumeTab.click();
    });
    await wait(220);
    await flush();

    expect(root.textContent).toContain("Codex history");
    expect(api.getSessionResumeCandidates).toHaveBeenCalledWith("/root/docs", "codex", { offset: 0, limit: 0, scanOffset: 0, scanLimit: 20 });
  });

  it("creates a session and selects the returned session id", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({ session_id: "new", broker_pid: 42, backend: "codex", ok: true } as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [
        { session_id: "old" },
        { session_id: "new" },
      ],
      activeSessionId: "old",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/project"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          pi: { provider_choice: "macaron", model: "gpt-5.4", reasoning_effort: "high" },
          codex: {
            provider_choice: "chatgpt",
            provider_choices: ["chatgpt", "openai-api"],
            model: "gpt-5",
            reasoning_effort: "medium",
            reasoning_efforts: ["medium", "high"],
            supports_fast: true,
          },
        },
      },
    });
    const onClose = vi.fn();

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={onClose} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    expect(root.querySelector('[data-testid="new-session-dialog"]')).not.toBeNull();
    expect(root.querySelector('[data-testid="backend-tab-codex"]')).not.toBeNull();
    expect(root.querySelector('input[name="cwd"]')).not.toBeNull();
    expect(root.querySelector('button[type="submit"]')).not.toBeNull();
    expect(root.textContent).toContain("Working directory");
    expect(root.textContent).toContain("Session name");
    expect(root.textContent).toContain("Model");
    expect(root.textContent).toContain("Provider");
    expect(root.textContent).toContain("Reasoning effort");
    expect(root.textContent).toContain("Launch mode");
    expect(root.textContent).not.toContain("Git worktree branch");
    expect(root.textContent).toContain("Speed");

    const cwdInput = root.querySelector('input[placeholder="/path/to/project"]') as HTMLInputElement;
    await setInputValue(cwdInput, "/tmp/project");
    await flush();

    const nameInput = root.querySelector('input[name="sessionName"]') as HTMLInputElement;
    await setInputValue(nameInput, "Inbox cleanup");
    await flush();

    const modelInput = root.querySelector('input[name="model"]') as HTMLInputElement;
    await setInputValue(modelInput, "gpt-5.4");
    await flush();

    const providerSelect = root.querySelector('select[name="providerChoice"]') as HTMLSelectElement;
    await setSelectValue(providerSelect, "openai-api");

    const reasoningSelect = root.querySelector('select[name="reasoningEffort"]') as HTMLSelectElement;
    await setSelectValue(reasoningSelect, "high");
    await flush();

    const fastCheckbox = root.querySelector('input[name="fastMode"]') as HTMLInputElement;
    const tmuxCheckbox = root.querySelector('input[name="createInTmux"]') as HTMLInputElement;
    await setCheckboxValue(fastCheckbox, true);
    await setCheckboxValue(tmuxCheckbox, true);
    await flush();

    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();

    expect(api.createSession).toHaveBeenCalledWith({
      cwd: "/tmp/project",
      title: "Inbox cleanup",
      agent_backend: "codex",
      provider: "openai-api",
      model: "gpt-5.4",
      reasoning_effort: undefined,
      resume_session_id: undefined,
    });
    expect(api.renameSession).not.toHaveBeenCalled();
    expect(sessionsStore.upsertSession).toHaveBeenCalledWith(expect.objectContaining({
      session_id: "new",
      agent_backend: "codex",
      cwd: "/tmp/project",
      alias: "Inbox cleanup",
    }), { prepend: true, select: true });
    expect(onClose).toHaveBeenCalled();
  });

  it("optimistically inserts pending Pi sessions instead of waiting for a full refresh", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({
      session_id: "pi-pending",
      backend: "pi",
      pending_startup: true,
      ok: true,
    } as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [{ session_id: "old", cwd: "/tmp/project", agent_backend: "pi" }],
      activeSessionId: "old",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/project"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: { provider_choice: "macaron", model: "gpt-5.4", reasoning_effort: "high" },
          codex: { provider_choice: "chatgpt" },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();

    expect(sessionsStore.upsertSession).toHaveBeenCalledWith(expect.objectContaining({
      session_id: "pi-pending",
      agent_backend: "pi",
      cwd: "/tmp/project",
      pending_startup: true,
      busy: true,
    }), { prepend: true, select: true });
    expect(sessionsStore.refresh).not.toHaveBeenCalled();
    expect(sessionsStore.getState().activeSessionId).toBe("pi-pending");
    expect(sessionsStore.getState().items[0]).toMatchObject({
      session_id: "pi-pending",
      pending_startup: true,
    });
  });

  it("does not render existing-session navigation inside NewSession", async () => {
    const sessionsStore = createSessionsStore({
      items: [
        { session_id: "sess-1", alias: "Inbox cleanup", cwd: "/tmp/project-a", agent_backend: "pi", focused: true },
        { session_id: "pi-history-1", alias: "ForkKV notes", cwd: "/root/docs", agent_backend: "pi", runtime_id: "" },
      ],
      activeSessionId: "sess-1",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/project-a"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          codex: { provider_choice: "chatgpt" },
          pi: {},
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    expect(Array.from(root.querySelectorAll("button")).some((button) => button.textContent?.trim() === "Focus")).toBe(false);
    expect(Array.from(root.querySelectorAll("button")).some((button) => button.textContent?.trim() === "Pi history")).toBe(false);
    expect(root.textContent).not.toContain("Inbox cleanup");
    expect(root.textContent).not.toContain("ForkKV notes");
    expect(sessionsStore.select).not.toHaveBeenCalled();
  });

  it("uses the active session cwd before recent cwds", async () => {
    const sessionsStore = createSessionsStore({
      items: [
        { session_id: "active", cwd: "/Users/demo/current-project" },
        { session_id: "other", cwd: "/Users/demo/other-project" },
      ],
      activeSessionId: "active",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/project"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: { provider_choice: "chatgpt" },
          pi: {},
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const cwdInput = root.querySelector('input[name="cwd"]') as HTMLInputElement;
    expect(cwdInput.value).toBe("/Users/demo/current-project");
  });

  it("selects and closes without a second rename request after launch", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({ session_id: "new", broker_pid: 42, backend: "codex", ok: true } as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [
        { session_id: "old" },
        { session_id: "new" },
      ],
      activeSessionId: "old",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/project"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: { provider_choice: "chatgpt" },
          pi: {},
        },
      },
    });
    const onClose = vi.fn();

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={onClose} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const input = root.querySelector('input[name="cwd"]') as HTMLInputElement;
    await setInputValue(input, "/tmp/project");
    const sessionNameInput = root.querySelector('input[name="sessionName"]') as HTMLInputElement;
    await setInputValue(sessionNameInput, "Inbox cleanup");
    await flush();

    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();

    expect(api.createSession).toHaveBeenCalledWith({
      cwd: "/tmp/project",
      title: "Inbox cleanup",
      agent_backend: "codex",
      provider: undefined,
      model: undefined,
      reasoning_effort: undefined,
      resume_session_id: undefined,
    });
    expect(api.renameSession).not.toHaveBeenCalled();
    expect(sessionsStore.upsertSession).toHaveBeenCalledWith(expect.objectContaining({
      session_id: "new",
      agent_backend: "codex",
      cwd: "/tmp/project",
      alias: "Inbox cleanup",
    }), { prepend: true, select: true });
    expect(sessionsStore.getState().activeSessionId).toBe("new");
    expect(onClose).toHaveBeenCalled();
  });

  it("ignores duplicate submit events while launch is already in progress", async () => {
    const { api } = await import("../../lib/api");
    let resolveCreate: (value: unknown) => void = () => undefined;
    const pendingCreate = new Promise((resolve) => {
      resolveCreate = resolve;
    });
    vi.mocked(api.createSession).mockReturnValueOnce(pendingCreate as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [{ session_id: "pending" }],
      activeSessionId: "pending",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/project"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: { provider_choice: "chatgpt" },
          pi: {},
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const input = root.querySelector('input[name="cwd"]') as HTMLInputElement;
    await setInputValue(input, "/tmp/project");
    await flush();

    const form = root.querySelector("form") as HTMLFormElement;
    await act(async () => {
      form.requestSubmit();
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });

    expect(api.createSession).toHaveBeenCalledTimes(1);

    resolveCreate({ session_id: "pending-new", broker_pid: 12, backend: "codex", ok: true });
    await flush();
  });

  it("hydrates late-arriving backend defaults for the selected backend without overwriting later user edits", async () => {
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      recentCwds: [],
      tmuxAvailable: true,
      newSessionDefaults: null,
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const piTab = root.querySelector('[data-testid="backend-tab-pi"]') as HTMLButtonElement;
    await act(async () => {
      piTab.click();
      await Promise.resolve();
    });

    act(() => {
      sessionsStore.setState({
        ...sessionsStore.getState(),
        newSessionDefaults: {
          default_backend: "codex",
          backends: {
            pi: { provider_choice: "macaron", model: "gpt-5.4", reasoning_effort: "high" },
            codex: { provider_choice: "chatgpt", model: "gpt-5", reasoning_effort: "medium", supports_fast: true },
          },
        },
      });
    });
    await flush();

    expect((root.querySelector('input[name="model"]') as HTMLInputElement).value).toBe("gpt-5.4");
    expect((root.querySelector('select[name="providerChoice"]') as HTMLSelectElement).value).toBe("macaron");
    expect((root.querySelector('select[name="reasoningEffort"]') as HTMLSelectElement).value).toBe("high");

    const modelInput = root.querySelector('input[name="model"]') as HTMLInputElement;
    await setInputValue(modelInput, "custom-model");
    await flush();

    act(() => {
      sessionsStore.setState({
        ...sessionsStore.getState(),
        newSessionDefaults: {
          default_backend: "pi",
          backends: {
            pi: { provider_choice: "other", model: "replacement", reasoning_effort: "low" },
            codex: { provider_choice: "chatgpt" },
          },
        },
      });
    });
    await flush();

    expect((root.querySelector('input[name="model"]') as HTMLInputElement).value).toBe("custom-model");
  });

  it("selects the returned new session id without renaming some other tab", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({ session_id: "new-from-server", broker_pid: 99, backend: "codex", ok: true } as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [
        { session_id: "first-existing" },
        { session_id: "second-existing" },
      ],
      activeSessionId: "first-existing",
      loading: false,
      recentCwds: ["/tmp/project"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: { provider_choice: "chatgpt" },
          pi: {},
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const cwdInput = root.querySelector('input[name="cwd"]') as HTMLInputElement;
    await setInputValue(cwdInput, "/tmp/project");
    await flush();

    const nameInput = root.querySelector('input[name="sessionName"]') as HTMLInputElement;
    await setInputValue(nameInput, "fresh-name");
    await flush();

    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();
    await flush();

    expect(api.createSession).toHaveBeenCalledWith({
      cwd: "/tmp/project",
      title: "fresh-name",
      agent_backend: "codex",
      provider: undefined,
      model: undefined,
      reasoning_effort: undefined,
      resume_session_id: undefined,
    });
    expect(api.renameSession).not.toHaveBeenCalled();
    expect(sessionsStore.upsertSession).toHaveBeenCalledWith(expect.objectContaining({
      session_id: "new-from-server",
      agent_backend: "codex",
      cwd: "/tmp/project",
      alias: "fresh-name",
    }), { prepend: true, select: true });
    expect(sessionsStore.getState().activeSessionId).toBe("new-from-server");
  });

  it("refreshes cached Pi model suggestions on demand", async () => {
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      recentCwds: [],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: {
            provider_choice: "macaron",
            provider_choices: ["macaron"],
            model: "gpt-5.4",
            models: ["gpt-5.4"],
            provider_models: {
              macaron: ["gpt-5.4"],
            },
            reasoning_effort: "high",
            reasoning_efforts: ["medium", "high"],
          } as any,
          codex: { provider_choice: "chatgpt" },
        },
      },
    });
    vi.mocked(sessionsStore.refreshBootstrap).mockImplementation(async (options?: { refreshPiModels?: boolean }) => {
      if (options?.refreshPiModels) {
        sessionsStore.setState({
          ...sessionsStore.getState(),
          bootstrapLoaded: true,
          newSessionDefaults: {
            default_backend: "pi",
            backends: {
              pi: {
                provider_choice: "macaron",
                provider_choices: ["macaron"],
                model: "gpt-5.4-mini",
                models: ["gpt-5.4-mini", "gpt-5.4"],
                provider_models: {
                  macaron: ["gpt-5.4-mini", "gpt-5.4"],
                },
                reasoning_effort: "high",
                reasoning_efforts: ["medium", "high"],
              } as any,
              codex: { provider_choice: "chatgpt" },
            },
          },
        });
        return;
      }
      sessionsStore.setState({ ...sessionsStore.getState(), bootstrapLoaded: true });
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    expect((root.querySelector('input[name="model"]') as HTMLInputElement).value).toBe("gpt-5.4");

    const refreshButton = Array.from(root.querySelectorAll("button")).find((button) => button.textContent?.includes("Refresh Pi models")) as HTMLButtonElement;
    expect(refreshButton).not.toBeNull();
    await act(async () => {
      refreshButton.click();
      await Promise.resolve();
    });
    await flush();

    expect(sessionsStore.refreshBootstrap).toHaveBeenCalledWith({ refreshPiModels: true });
    expect(Array.from(root.querySelectorAll('#new-session-models option')).map((option) => option.getAttribute("value"))).toEqual([
      "gpt-5.4-mini",
      "gpt-5.4",
    ]);
  });

  it("updates Pi model suggestions when the provider changes", async () => {
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      recentCwds: [],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: {
            provider_choice: "macaron",
            provider_choices: ["macaron", "anthropic"],
            model: "gpt-5.4",
            models: ["gpt-5.4", "gpt-5.3-codex"],
            provider_models: {
              macaron: ["gpt-5.4", "gpt-5.3-codex"],
              anthropic: ["claude-sonnet-4-6", "claude-opus-4-6"],
            },
            reasoning_effort: "high",
            reasoning_efforts: ["medium", "high"],
          } as any,
          codex: { provider_choice: "chatgpt" },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const providerSelect = root.querySelector('select[name="providerChoice"]') as HTMLSelectElement;
    expect(Array.from(root.querySelectorAll('#new-session-models option')).map((option) => option.getAttribute("value"))).toEqual([
      "gpt-5.4",
      "gpt-5.3-codex",
    ]);

    await setSelectValue(providerSelect, "anthropic");
    await flush();

    expect(Array.from(root.querySelectorAll('#new-session-models option')).map((option) => option.getAttribute("value"))).toEqual([
      "claude-sonnet-4-6",
      "claude-opus-4-6",
    ]);
  });

  it("only replaces the Pi model value on provider change when the model input is untouched", async () => {
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      recentCwds: [],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: {
            provider_choice: "macaron",
            provider_choices: ["macaron", "anthropic"],
            model: "gpt-5.4",
            models: ["gpt-5.4", "gpt-5.3-codex"],
            provider_models: {
              macaron: ["gpt-5.4", "gpt-5.3-codex"],
              anthropic: ["claude-sonnet-4-6", "claude-opus-4-6"],
            },
            reasoning_effort: "high",
            reasoning_efforts: ["medium", "high"],
          } as any,
          codex: { provider_choice: "chatgpt" },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const modelInput = root.querySelector('input[name="model"]') as HTMLInputElement;
    const providerSelect = root.querySelector('select[name="providerChoice"]') as HTMLSelectElement;

    expect(modelInput.value).toBe("gpt-5.4");
    await setSelectValue(providerSelect, "anthropic");
    await flush();
    expect(modelInput.value).toBe("claude-sonnet-4-6");

    await setInputValue(modelInput, "custom-model");
    await setSelectValue(providerSelect, "macaron");
    await flush();
    expect(modelInput.value).toBe("custom-model");
  });

  it("keeps Pi provider changes auto-updating the model until the user edits it after switching backends", async () => {
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      recentCwds: [],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: { provider_choice: "chatgpt", model: "gpt-5" },
          pi: {
            provider_choice: "macaron",
            provider_choices: ["macaron", "anthropic"],
            model: "gpt-5.4",
            models: ["gpt-5.4", "gpt-5.3-codex"],
            provider_models: {
              macaron: ["gpt-5.4", "gpt-5.3-codex"],
              anthropic: ["claude-sonnet-4-6", "claude-opus-4-6"],
            },
            reasoning_effort: "high",
            reasoning_efforts: ["medium", "high"],
          } as any,
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const piTab = root.querySelector('[data-testid="backend-tab-pi"]') as HTMLButtonElement;
    await act(async () => {
      piTab.click();
      await Promise.resolve();
    });
    await flush();

    const modelInput = root.querySelector('input[name="model"]') as HTMLInputElement;
    const providerSelect = root.querySelector('select[name="providerChoice"]') as HTMLSelectElement;

    expect(modelInput.value).toBe("gpt-5.4");
    await setSelectValue(providerSelect, "anthropic");
    await flush();
    expect(modelInput.value).toBe("claude-sonnet-4-6");

    await setInputValue(modelInput, "custom-model");
    await setSelectValue(providerSelect, "macaron");
    await flush();
    expect(modelInput.value).toBe("custom-model");
  });

  it("uses Codex provider-scoped models when provider changes", async () => {
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      recentCwds: [],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: {
            provider_choice: "crs",
            provider_choices: ["crs", "chatgpt"],
            model: "gpt-5.5",
            models: ["gpt-5.5"],
            provider_models: {
              crs: ["gpt-5.5", "gpt-5.4"],
              chatgpt: ["gpt-5.3-codex"],
            },
            reasoning_effort: "high",
          },
          pi: { provider_choice: "macaron" },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const modelInput = root.querySelector('input[name="model"]') as HTMLInputElement;
    const providerSelect = root.querySelector('select[name="providerChoice"]') as HTMLSelectElement;

    expect(modelInput.value).toBe("gpt-5.5");
    expect(root.querySelector('option[value="gpt-5.4"]')).not.toBeNull();

    await setSelectValue(providerSelect, "chatgpt");
    await flush();
    expect(modelInput.value).toBe("gpt-5.3-codex");

    await setInputValue(modelInput, "custom-codex");
    await setSelectValue(providerSelect, "crs");
    await flush();
    expect(modelInput.value).toBe("custom-codex");
  });

  it("keeps the working directory when session defaults refresh while the dialog is open", async () => {
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      recentCwds: ["/tmp/project", "/tmp/other"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "codex",
        backends: {
          codex: {
            provider_choice: "chatgpt",
            provider_choices: ["chatgpt", "openai-api"],
            model: "gpt-5",
            reasoning_effort: "medium",
            reasoning_efforts: ["medium", "high"],
            supports_fast: true,
          },
          pi: { provider_choice: "macaron" },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const cwdInput = root.querySelector('input[placeholder="/path/to/project"]') as HTMLInputElement;
    await setInputValue(cwdInput, "/tmp/project");
    await flush();

    await act(async () => {
      sessionsStore.setState({
        ...sessionsStore.getState(),
        recentCwds: ["/tmp/project", "/tmp/other", "/tmp/new"],
        newSessionDefaults: {
          default_backend: "codex",
          backends: {
            codex: {
              provider_choice: "chatgpt",
              provider_choices: ["chatgpt", "openai-api"],
              model: "gpt-5",
              reasoning_effort: "medium",
              reasoning_efforts: ["medium", "high"],
              supports_fast: true,
            },
            pi: { provider_choice: "macaron" },
          },
        },
      });
    });
    await flush();

    expect(cwdInput.value).toBe("/tmp/project");
  });

  it("pages older resume candidates and prefers persisted Pi session titles", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.getSessionResumeCandidates).mockImplementation(async (_cwd, _backend, options) => {
      if ((options?.scanOffset || 0) > 0) {
        return {
          exists: true,
          will_create: false,
          git_repo: false,
          offset: 0,
          limit: 0,
          remaining: 0,
          scan_offset: 20,
          scanned: 1,
          scan_remaining: 0,
          scan_complete: true,
          sessions: [{ session_id: "older-1", title: "older-title", first_user_message: "older prompt", updated_ts: 1_760_000_100 }],
        } as any;
      }
      return {
        exists: true,
        will_create: false,
        git_repo: false,
        offset: 0,
        limit: 0,
        remaining: 0,
        scan_offset: 0,
        scanned: 20,
        scan_remaining: 20,
        scan_complete: false,
        sessions: [{ session_id: "recent-1", display_name: "pi-slash-name", title: "first prompt title", first_user_message: "recent prompt", updated_ts: 1_760_000_200 }],
      } as any;
    });

    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/pi-project"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: { provider_choice: "macaron" },
          codex: { provider_choice: "chatgpt" },
        },
        backend_capabilities: {
          pi: { resume_history: true },
          codex: { resume_history: false },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    const resumeTab = Array.from(root.querySelectorAll("button")).find((node) => node.textContent?.trim() === "Resume") as HTMLButtonElement;
    await act(async () => {
      resumeTab.click();
    });
    const cwdInput = root.querySelector('input[name="cwd"]') as HTMLInputElement;
    await setInputValue(cwdInput, "/tmp/pi-project");
    await wait(220);
    await flush();

    expect(root.textContent).toContain("pi-slash-name");
    expect(root.textContent).not.toContain("first prompt title");
    expect(root.textContent).toContain("recent prompt");
    expect(root.textContent).toContain(new Date(1_760_000_200 * 1000).toLocaleString());
    expect(root.textContent).toContain("Inspected 1-20");
    expect(root.textContent).toContain("1 shown");
    expect(root.textContent).toContain("20 older uninspected");

    const olderButton = Array.from(root.querySelectorAll("button")).find((node) => node.textContent?.trim() === "Older") as HTMLButtonElement;
    await act(async () => {
      olderButton.click();
    });
    await wait(220);
    await flush();

    expect(vi.mocked(api.getSessionResumeCandidates)).toHaveBeenLastCalledWith("/tmp/pi-project", "pi", { offset: 0, limit: 0, scanOffset: 20, scanLimit: 20 });
    expect(root.textContent).toContain("older-title");
    expect(root.textContent).toContain("Inspected 21-21");
    expect(root.textContent).toContain("1 shown");
  });

  it("creates a new ActRail slot from a selected resume candidate", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({ session_id: "resumed-pi", backend: "pi", ok: true } as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      offset: 0,
      limit: 20,
      remaining: 0,
      sessions: [{ session_id: "history:pi:abc", title: "ForkKV", first_user_message: "resume me" }],
    } as any);
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/root/docs"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: { provider_choice: "macaron", model: "gpt-5.4", reasoning_effort: "high" },
        },
        backend_capabilities: {
          pi: { resume_history: true },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    const resumeTab = Array.from(root.querySelectorAll("button")).find((node) => node.textContent?.trim() === "Resume") as HTMLButtonElement;
    await act(async () => {
      resumeTab.click();
    });
    await wait(220);
    await flush();

    const candidate = Array.from(root.querySelectorAll<HTMLButtonElement>(".focusSessionItem")).find((node) => node.textContent?.includes("ForkKV"));
    expect(candidate).toBeDefined();
    await act(async () => {
      candidate?.click();
    });
    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();

    expect(api.createSession).toHaveBeenCalledWith({
      cwd: "/root/docs",
      title: undefined,
      agent_backend: "pi",
      provider: "macaron",
      model: "gpt-5.4",
      reasoning_effort: "high",
      resume_session_id: "history:pi:abc",
      pi_agent_grpc: true,
    });
  });

  it("clears stale resume selection immediately when the cwd changes", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({ session_id: "new-pi", broker_pid: 91, backend: "pi", ok: true } as any);
    vi.mocked(api.getSessionResumeCandidates).mockImplementation(async (cwd) => {
      if (cwd === "/tmp/old-project") {
        return {
          exists: true,
          will_create: false,
          git_repo: false,
          offset: 0,
          limit: 20,
          remaining: 0,
          sessions: [{ session_id: "stale-resume", title: "actrail new 修复", first_user_message: "old prompt" }],
        } as any;
      }
      return {
        exists: true,
        will_create: false,
        git_repo: false,
        offset: 0,
        limit: 20,
        remaining: 0,
        sessions: [],
      } as any;
    });

    const sessionsStore = createSessionsStore({
      items: [{ session_id: "old-live" }, { session_id: "new-pi" }],
      activeSessionId: null,
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/old-project"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: { provider_choice: "macaron", model: "gpt-5.4", reasoning_effort: "high" },
          codex: { provider_choice: "chatgpt" },
        },
        backend_capabilities: {
          pi: { resume_history: true },
          codex: { resume_history: false },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    const resumeTab = Array.from(root.querySelectorAll("button")).find((node) => node.textContent?.trim() === "Resume") as HTMLButtonElement;
    await act(async () => {
      resumeTab.click();
    });
    const cwdInput = root.querySelector('input[name="cwd"]') as HTMLInputElement;
    await setInputValue(cwdInput, "/tmp/old-project");
    await wait(220);
    await flush();

    expect(root.textContent).toContain("actrail new 修复");

    const candidate = Array.from(root.querySelectorAll<HTMLButtonElement>(".focusSessionItem")).find((node) => node.textContent?.includes("actrail new 修复"));
    expect(candidate).toBeDefined();
    await act(async () => {
      candidate?.click();
    });
    expect(root.textContent).toContain("Selected: actrail new 修复");

    await setInputValue(cwdInput, "/tmp/new-project");
    await flush();

    expect(root.textContent).not.toContain("actrail new 修复");
    expect(root.textContent).not.toContain("Selected: actrail new 修复");

    const startTab = Array.from(root.querySelectorAll("button")).find((node) => node.textContent?.trim() === "Start") as HTMLButtonElement;
    await act(async () => {
      startTab.click();
    });
    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();

    expect(api.createSession).toHaveBeenCalledWith({
      cwd: "/tmp/new-project",
      title: undefined,
      agent_backend: "pi",
      provider: "macaron",
      model: "gpt-5.4",
      reasoning_effort: "high",
      resume_session_id: undefined,
      pi_agent_grpc: true,
    });
  });

  it("lets Pi sessions launch in tmux and explains the pi-rpc host split", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({ session_id: "pi-new", broker_pid: 84, backend: "pi", ok: true } as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({
      exists: true,
      will_create: false,
      git_repo: false,
      sessions: [],
    } as any);
    vi.mocked(api.renameSession).mockResolvedValue({ ok: true } as any);

    const sessionsStore = createSessionsStore({
      items: [{ session_id: "old" }, { session_id: "pi-new" }],
      activeSessionId: "old",
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/pi-project"],
      tmuxAvailable: true,
      newSessionDefaults: {
        default_backend: "pi",
        backends: {
          pi: {
            provider_choice: "macaron",
            provider_choices: ["macaron", "anthropic"],
            model: "gpt-5.4",
            reasoning_effort: "high",
            reasoning_efforts: ["medium", "high"],
          },
          codex: { provider_choice: "chatgpt" },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const cwdInput = root.querySelector('input[name="cwd"]') as HTMLInputElement;
    await setInputValue(cwdInput, "/tmp/pi-project");
    await flush();

    const tmuxCheckbox = root.querySelector('input[name="createInTmux"]') as HTMLInputElement;
    expect(tmuxCheckbox.disabled).toBe(false);
    expect(root.textContent).toContain("Host the new Pi session in tmux while pi-rpc handles web control.");

    await setCheckboxValue(tmuxCheckbox, true);
    await flush();

    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();

    expect(api.createSession).toHaveBeenCalledWith({
      cwd: "/tmp/pi-project",
      title: undefined,
      agent_backend: "pi",
      provider: "macaron",
      model: "gpt-5.4",
      reasoning_effort: "high",
      resume_session_id: undefined,
      pi_agent_grpc: true,
    });
  });

  it("sends false when the Pi gRPC transport is disabled", async () => {
    const { api } = await import("../../lib/api");
    vi.mocked(api.createSession).mockResolvedValue({ session_id: "pi-iod", backend: "pi", ok: true } as any);
    vi.mocked(api.getSessionResumeCandidates).mockResolvedValue({ exists: true, will_create: false, git_repo: false, sessions: [] } as any);
    const sessionsStore = createSessionsStore({
      items: [],
      activeSessionId: null,
      loading: false,
      bootstrapLoaded: true,
      recentCwds: ["/tmp/pi-project"],
      tmuxAvailable: false,
      newSessionDefaults: {
        default_backend: "pi",
        pi_agent_grpc_default: true,
        backends: {
          pi: { provider_choice: "macaron", model: "gpt-5.4", reasoning_effort: "high" },
        },
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any}>
          <NewSessionDialog open onClose={() => undefined} />
        </AppProviders>,
        root!,
      );
    });
    await flush();

    const grpcCheckbox = root.querySelector('input[name="usePIAgentGRPC"]') as HTMLInputElement;
    expect(grpcCheckbox.checked).toBe(true);
    await setCheckboxValue(grpcCheckbox, false);
    await flush();

    const form = root.querySelector("form") as HTMLFormElement;
    await submitForm(form);
    await flush();

    expect(api.createSession).toHaveBeenCalledWith(expect.objectContaining({
      agent_backend: "pi",
      pi_agent_grpc: false,
    }));
  });
});
