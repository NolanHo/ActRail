/** @jsxImportSource preact */
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import { useAppShellAudio } from "./useAppShellAudio";

vi.mock("../../lib/api", () => ({
  api: {
    getVoiceSettings: vi.fn().mockResolvedValue({ ok: true }),
    saveVoiceSettings: vi.fn().mockResolvedValue({ ok: true }),
    testVoiceProvider: vi.fn().mockResolvedValue({ ok: true }),
    setAudioListener: vi.fn().mockResolvedValue({ ok: true }),
    triggerTestAnnouncement: vi.fn().mockResolvedValue({ ok: true }),
  },
}));

function Harness({ bootstrapLoaded = true, voiceSupported = true }: { bootstrapLoaded?: boolean; voiceSupported?: boolean }) {
  const state = useAppShellAudio({ bootstrapLoaded, voiceSupported });
  return (
    <div>
      <div data-label={state.announcementLabel} data-status={state.voiceSettingsStatus} />
      <button type="button" onClick={() => state.openVoiceSettings()}>Open</button>
    </div>
  );
}

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

afterEach(() => {
  document.body.innerHTML = "";
  localStorage.clear();
  vi.clearAllMocks();
});

it("skips voice routes when bootstrap disables announcements", async () => {
  const { api } = await import("../../lib/api");
  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(<Harness bootstrapLoaded voiceSupported={false} />, root);
    await flush();
  });

  expect(api.getVoiceSettings).not.toHaveBeenCalled();
  expect(api.setAudioListener).not.toHaveBeenCalled();
  expect(root.querySelector("[data-label]")?.getAttribute("data-label")).toBe("Announcements unavailable");
});
