import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

import { VoiceSettingsDialog } from "./VoiceSettingsDialog";

describe("VoiceSettingsDialog", () => {
  let root: HTMLDivElement | null = null;

  afterEach(() => {
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
  });

  it("uses the shared dialog action button styles instead of legacy button classes", () => {
    root = document.createElement("div");
    document.body.appendChild(root);

    render(
      <VoiceSettingsDialog
        audioMeta={{ enabledDevices: 0, lastError: "", listeners: 0, queue: 0, segments: 0, totalDevices: 0 }}
        enterToSendDraft={false}
        narrationEnabledDraft={false}
        open
        replySoundEnabled={false}
        status=""
        themeMode="light"
        voiceApiKeyDraft=""
        voiceBaseUrlDraft=""
        onChangeEnterToSend={() => undefined}
        onChangeNarrationEnabled={() => undefined}
        onChangeReplySoundEnabled={() => undefined}
        onChangeThemeMode={() => undefined}
        onChangeVoiceApiKey={() => undefined}
        onChangeVoiceBaseUrl={() => undefined}
        onClose={() => undefined}
        onSave={() => undefined}
        onTestProvider={() => undefined}
        onTriggerTestPush={() => undefined}
      />,
      root,
    );

    const buttons = Array.from(root.querySelectorAll("button"));
    const testPushButton = buttons.find((button) => button.textContent === "Test Push");
    const cancelButton = buttons.find((button) => button.textContent === "Cancel");
    const saveButton = buttons.find((button) => button.textContent === "Save");

    expect(testPushButton?.className).toContain("border");
    expect(testPushButton?.className).toContain("bg-background");
    expect(testPushButton?.className).not.toContain("secondaryButton");

    expect(cancelButton?.className).toContain("border");
    expect(cancelButton?.className).toContain("bg-background");
    expect(cancelButton?.className).not.toContain("secondaryButton");

    expect(saveButton?.className).toContain("bg-primary");
    expect(saveButton?.className).toContain("text-primary-foreground");
    expect(saveButton?.className).not.toContain("primaryButton");
  });

  it("shows and toggles experimental transport state in the status tab", async () => {
    const onChangeTransportOptIn = vi.fn();
    const onChangeConnectWireFormat = vi.fn();
    root = document.createElement("div");
    document.body.appendChild(root);

    render(
      <VoiceSettingsDialog
        audioMeta={{ enabledDevices: 0, lastError: "", listeners: 0, queue: 0, segments: 0, totalDevices: 0 }}
        enterToSendDraft={false}
        narrationEnabledDraft={false}
        open
        replySoundEnabled={false}
        status=""
        themeMode="light"
        transportStatus={{
          active: "connect",
          connectAvailable: true,
          connectOptIn: true,
          desktopEligible: true,
          connectPath: "/api/connect",
          wireFormat: "json",
        }}
        voiceApiKeyDraft=""
        voiceBaseUrlDraft=""
        onChangeEnterToSend={() => undefined}
        onChangeNarrationEnabled={() => undefined}
        onChangeReplySoundEnabled={() => undefined}
        onChangeThemeMode={() => undefined}
        onChangeTransportOptIn={onChangeTransportOptIn}
        onChangeConnectWireFormat={onChangeConnectWireFormat}
        onChangeVoiceApiKey={() => undefined}
        onChangeVoiceBaseUrl={() => undefined}
        onClose={() => undefined}
        onSave={() => undefined}
        onTestProvider={() => undefined}
        onTriggerTestPush={() => undefined}
      />,
      root,
    );

    await act(async () => {
      Array.from(root!.querySelectorAll("button")).find((button) => button.textContent === "Status")?.click();
    });

    expect(root.textContent).toContain("Realtime transport: ConnectRPC experimental");
    expect(root.textContent).toContain("Connect opt-in: on");
    expect(root.textContent).toContain("Connect wire format: json");
    expect(root.textContent).toContain("Connect capability: available");
    expect(root.textContent).toContain("Desktop eligible: yes");
    expect(root.textContent).toContain("Connect path: /api/connect");
    const transportToggle = Array.from(root.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
      .find((input) => input.parentElement?.textContent?.includes("Use ConnectRPC transport on desktop"));
    expect(transportToggle?.checked).toBe(true);
    transportToggle!.checked = false;
    transportToggle!.dispatchEvent(new Event("change", { bubbles: true }));
    expect(onChangeTransportOptIn).toHaveBeenCalledWith(false);
    const protoToggle = Array.from(root.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
      .find((input) => input.parentElement?.textContent?.includes("Use protobuf envelopes"));
    expect(protoToggle?.checked).toBe(false);
    protoToggle!.checked = true;
    protoToggle!.dispatchEvent(new Event("change", { bubbles: true }));
    expect(onChangeConnectWireFormat).toHaveBeenCalledWith("proto");
  });

  it("wires action callbacks through the footer controls", async () => {
    const onClose = vi.fn();
    const onSave = vi.fn();
    const onTriggerTestPush = vi.fn();
    const onTestProvider = vi.fn();
    const onChangeThemeMode = vi.fn();

    root = document.createElement("div");
    document.body.appendChild(root);

    render(
      <VoiceSettingsDialog
        audioMeta={{ enabledDevices: 0, lastError: "", listeners: 0, queue: 0, segments: 0, totalDevices: 0 }}
        enterToSendDraft={false}
        narrationEnabledDraft={false}
        open
        replySoundEnabled={false}
        status=""
        themeMode="light"
        voiceApiKeyDraft=""
        voiceBaseUrlDraft=""
        onChangeEnterToSend={() => undefined}
        onChangeNarrationEnabled={() => undefined}
        onChangeReplySoundEnabled={() => undefined}
        onChangeThemeMode={onChangeThemeMode}
        onChangeVoiceApiKey={() => undefined}
        onChangeVoiceBaseUrl={() => undefined}
        onClose={onClose}
        onSave={onSave}
        onTestProvider={onTestProvider}
        onTriggerTestPush={onTriggerTestPush}
      />,
      root,
    );

    const buttons = Array.from(root.querySelectorAll("button"));
    buttons.find((button) => button.textContent === "Test Provider")?.click();
    buttons.find((button) => button.textContent === "Test Push")?.click();
    buttons.find((button) => button.textContent === "Cancel")?.click();
    buttons.find((button) => button.textContent === "Save")?.click();
    await act(async () => {
      buttons.find((button) => button.textContent === "Display")?.click();
    });
    const darkRadio = Array.from(root.querySelectorAll<HTMLInputElement>('input[type="radio"]')).find((input) => !input.checked);
    darkRadio?.click();

    expect(onTestProvider).toHaveBeenCalledTimes(1);
    expect(onTriggerTestPush).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onChangeThemeMode).toHaveBeenCalledWith("dark");
  });
});
