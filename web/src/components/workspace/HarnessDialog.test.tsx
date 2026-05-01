import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import { HarnessDialog } from "./HarnessDialog";

vi.mock("../../lib/api", () => ({
  api: {
    getHarness: vi.fn().mockResolvedValue({ ok: true, enabled: true }),
    saveHarness: vi.fn().mockResolvedValue({ ok: true, enabled: true }),
    getSupervisorProvider: vi.fn().mockResolvedValue({ ok: true, base_url: "", model: "", api_key_configured: false, complete: false }),
    saveSupervisorProvider: vi.fn().mockResolvedValue({ ok: true, base_url: "", model: "", api_key_configured: false, complete: false }),
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

it("does not call harness routes when the backend disables supervisor", async () => {
  const { api } = await import("../../lib/api");
  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(<HarnessDialog open sessionId="sess-1" supported={false} onClose={() => undefined} />, root);
    await flush();
  });

  expect(api.getSupervisorProvider).not.toHaveBeenCalled();
  expect(api.getSessionSupervisor).not.toHaveBeenCalled();
  expect(root.textContent).toContain("Supervisor is unavailable on this backend.");
});
