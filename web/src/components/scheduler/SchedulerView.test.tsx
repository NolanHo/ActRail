import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../../lib/api";
import { SchedulerView } from "./SchedulerView";

vi.mock("../../lib/api", () => ({
  api: {
    getScheduler: vi.fn().mockResolvedValue({ ok: true, settings: { idle_before_delivery_seconds: 30 }, items: [], inbox: [] }),
    saveSchedulerSettings: vi.fn().mockResolvedValue({ idle_before_delivery_seconds: 45 }),
    createSelfReminder: vi.fn().mockResolvedValue({ ok: true, self_reminder: { item_id: "self_reminder_1", session_id: "sess-1", kind: "self_reminder", state: "scheduled", due_ts: 0, created_ts: 0, updated_ts: 0 } }),
    listSessions: vi.fn().mockResolvedValue({ items: [{ session_id: "sess-1", alias: "Pi Work", agent_backend: "pi" }, { session_id: "sess-2", alias: "Codex Work", agent_backend: "codex" }] }),
    getSupervisorProvider: vi.fn().mockResolvedValue({ ok: true, base_url: "https://llm.test/v1", model: "model-a", api_key_configured: true, complete: true }),
    saveSupervisorProvider: vi.fn().mockResolvedValue({ ok: true, base_url: "https://llm.test/v1", model: "model-b", api_key_configured: true, complete: true }),
    testSupervisorProvider: vi.fn().mockResolvedValue({ ok: true, status: "provider chat completion succeeded", status_code: 200, output: "hello from test model" }),
    getSessionSupervisor: vi.fn().mockResolvedValue({ ok: true, supported: true, enabled: true, status: "idle", idle_after_minutes: 5, max_consecutive_injections: 10, consecutive_injections: 0, goal: "", acceptance_criteria: "", context_files: [] }),
    saveSessionSupervisor: vi.fn().mockResolvedValue({ ok: true, supported: true, enabled: true, status: "idle", idle_after_minutes: 2, max_consecutive_injections: 3, consecutive_injections: 0, goal: "Finish", acceptance_criteria: "", context_files: [] }),
    getSupervisorRuns: vi.fn().mockResolvedValue({ ok: true, runs: [] }),
    runSupervisorOnce: vi.fn().mockResolvedValue({ ok: true, run: { run_id: "supervisor_1", anchor_assistant_event_id: "pi:message:a1", status: "stop", reason: "complete" } }),
  },
}));

async function flush() {
  for (let index = 0; index < 8; index++) {
    await Promise.resolve();
  }
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function inputByLabel(root: HTMLElement, label: string) {
  const labels = Array.from(root.querySelectorAll("label"));
  const found = labels.find((item) => item.textContent?.includes(label));
  if (!found) throw new Error(`missing label ${label}`);
  const input = found.querySelector("input, textarea, select") as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement | null;
  if (!input) throw new Error(`missing input for ${label}`);
  return input;
}

function clickTestId(root: HTMLElement, testId: string) {
  const button = root.querySelector(`[data-testid="${testId}"]`) as HTMLButtonElement | null;
  if (!button) throw new Error(`missing button ${testId}`);
  button.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
}

async function waitForAssertion(assertion: () => void) {
  let lastError: unknown;
  for (let index = 0; index < 20; index++) {
    try {
      assertion();
      return;
    } catch (error) {
      lastError = error;
      await act(async () => {
        await flush();
      });
    }
  }
  throw lastError;
}

async function setValue(input: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement, value: string) {
  await act(async () => {
    input.value = value;
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
    await flush();
  });
}

describe("SchedulerView", () => {
  afterEach(() => {
    document.body.innerHTML = "";
    vi.clearAllMocks();
    vi.mocked(api.getScheduler).mockResolvedValue({ ok: true, settings: { idle_before_delivery_seconds: 30 }, items: [], inbox: [] });
  });

  it("loads scheduler, sessions, and provider", async () => {
    vi.mocked(api.getScheduler).mockResolvedValue({
      ok: true,
      settings: { idle_before_delivery_seconds: 30 },
      items: [],
      inbox: [
        {
          item_id: "inbox-1",
          session_id: "sess-1",
          source: "manual",
          title: "Manual follow-up",
          message: "inspect stuck session",
          due_ts: 1_775_000_000,
          state: "pending",
          created_ts: 1_775_000_000,
          updated_ts: 1_775_000_000,
        },
      ],
    } as any);
    const root = document.createElement("div");
    document.body.appendChild(root);

    await act(async () => {
      render(<SchedulerView />, root);
      await flush();
    });

    expect(api.getScheduler).toHaveBeenCalledWith(100);
    expect(api.listSessions).toHaveBeenCalledWith({ limit: 100 }, undefined, false);
    expect(api.getSupervisorProvider).toHaveBeenCalled();
    await waitForAssertion(() => {
      expect(root.textContent).toContain("Create self-reminder");
      expect(root.textContent).toContain("Supervisor provider");
      expect(root.textContent).toContain("All pending, blocked, delivered, and cancelled inbox messages across sessions.");
      expect(root.textContent).toContain("inspect stuck session");
      expect(root.textContent).toContain("Pi Work (pi)");
    });
  });

  it("saves settings and creates a self-reminder from the global view", async () => {
    const root = document.createElement("div");
    document.body.appendChild(root);

    await act(async () => {
      render(<SchedulerView />, root);
      await flush();
    });
    await setValue(inputByLabel(root, "Idle before delivery"), "45");
    await act(async () => {
      clickTestId(root, "scheduler-settings-save");
      await flush();
    });
    await setValue(inputByLabel(root, "Message"), "check build");
    await act(async () => {
      clickTestId(root, "scheduler-self-reminder-create");
      await flush();
    });

    await waitForAssertion(() => {
      expect(api.saveSchedulerSettings).toHaveBeenCalledWith({ idle_before_delivery_seconds: 45 });
      expect(api.createSelfReminder).toHaveBeenCalledWith({ session_id: "sess-1", duration_seconds: 60, title: "Self Reminder", message: "check build" });
    });
  });

  it("saves and tests supervisor provider", async () => {
    const root = document.createElement("div");
    document.body.appendChild(root);

    await act(async () => {
      render(<SchedulerView />, root);
      await flush();
    });
    await setValue(inputByLabel(root, "Model"), "model-b");
    await act(async () => {
      clickTestId(root, "supervisor-provider-save");
      await flush();
    });
    await act(async () => {
      clickTestId(root, "supervisor-provider-test");
      await flush();
    });

    await waitForAssertion(() => {
      expect(api.saveSupervisorProvider).toHaveBeenCalledWith({ base_url: "https://llm.test/v1", model: "model-b" });
      expect(api.testSupervisorProvider).toHaveBeenCalledWith({ base_url: "https://llm.test/v1", model: "model-b" });
    });
    expect(root.textContent).toContain("Test passed");
  });
});
