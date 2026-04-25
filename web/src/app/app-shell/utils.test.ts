import { describe, expect, it } from "vitest";
import {
  applyThemeMode,
  base64UrlToUint8Array,
  mergeVoiceSettings,
  readLocalToggle,
  readLocalToggleDefaultOn,
  readThemeMode,
  replySoundTextKey,
  shortSessionId,
  writeThemeMode,
} from "./utils";

describe("app-shell utils", () => {
  it("shortens UUID-like session ids to eight characters", () => {
    expect(shortSessionId("12345678-1234-1234-1234-123456789abc")).toBe("12345678");
    expect(shortSessionId("short-id")).toBe("short-id");
  });

  it("reads local toggle values with the current defaults", () => {
    localStorage.setItem("codoxear.flag", "1");

    expect(readLocalToggle("codoxear.flag")).toBe(true);

    localStorage.removeItem("codoxear.flag");
    expect(readLocalToggleDefaultOn("codoxear.default-on")).toBe(true);
  });

  it("reads and applies persisted theme mode", () => {
    writeThemeMode("dark");
    applyThemeMode(readThemeMode());

    expect(readThemeMode()).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(document.documentElement.style.colorScheme).toBe("dark");
  });

  it("merges voice settings into the default nested shape", () => {
    expect(mergeVoiceSettings(null).audio?.stream_url).toBe("/api/audio/live.m3u8");
  });

  it("normalizes reply-sound text keys", () => {
    expect(replySoundTextKey("sess-1", { notification_text: "done   now" })).toBe("session:sess-1:text:done now");
  });

  it("decodes base64url strings", () => {
    expect(Array.from(base64UrlToUint8Array("ZmFrZS1rZXk"))).toEqual(Array.from(new TextEncoder().encode("fake-key")));
  });
});
