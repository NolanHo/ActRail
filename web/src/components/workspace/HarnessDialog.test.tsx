import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import { HarnessDialog } from "./HarnessDialog";

vi.mock("../../lib/api", () => ({
  api: {
    getSessionSupervisor: vi.fn().mockResolvedValue({ ok: true, supported: true, enabled: true, status: "idle", idle_after_minutes: 5, max_consecutive_injections: 10, consecutive_injections: 0, context_files: [] }),
    saveSessionSupervisor: vi.fn().mockResolvedValue({ ok: true, supported: true, enabled: true, status: "idle", idle_after_minutes: 5, max_consecutive_injections: 10, consecutive_injections: 0, context_files: [] }),
  },
}));

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

afterEach(() => {
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

it("loads supervisor settings from the backend instead of using a frontend capability gate", async () => {
  const { api } = await import("../../lib/api");
  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(<HarnessDialog open sessionId="sess-1" onClose={() => undefined} />, root);
    await flush();
  });

  expect(api.getSessionSupervisor).toHaveBeenCalledWith("sess-1");
  expect(root.textContent).toContain("Session policy");
  expect(root.textContent).not.toContain("Supervisor is unavailable on this backend.");
});
