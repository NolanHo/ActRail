import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import { HarnessDialog } from "./HarnessDialog";

vi.mock("../../lib/api", () => ({
  api: {
    getHarness: vi.fn().mockResolvedValue({ ok: true, enabled: true }),
    saveHarness: vi.fn().mockResolvedValue({ ok: true, enabled: true }),
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

it("does not call harness routes when the backend disables harness", async () => {
  const { api } = await import("../../lib/api");
  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(<HarnessDialog open sessionId="sess-1" supported={false} onClose={() => undefined} />, root);
    await flush();
  });

  expect(api.getHarness).not.toHaveBeenCalled();
  expect(root.textContent).toContain("Harness is unavailable on this backend.");
});
