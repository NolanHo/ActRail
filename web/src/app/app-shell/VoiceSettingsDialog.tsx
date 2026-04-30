import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { RealtimeTransportStatus } from "../../domains/sessions/store";
import type { ThemeMode } from "./utils";

interface VoiceSettingsDialogProps {
  audioMeta: {
    enabledDevices: number;
    lastError: string;
    listeners: number;
    queue: number;
    segments: number;
    totalDevices: number;
  };
  enterToSendDraft: boolean;
  conversationFontSizePxDraft?: number;
  composerFontSizePxDraft?: number;
  bufferAssistantOutputDraft?: boolean;
  narrationEnabledDraft: boolean;
  open: boolean;
  replySoundEnabled: boolean;
  status: string;
  themeMode: ThemeMode;
  transportStatus?: RealtimeTransportStatus;
  voiceApiKeyDraft: string;
  voiceBaseUrlDraft: string;
  onChangeEnterToSend(value: boolean): void;
  onChangeConversationFontSizePx?(value: number): void;
  onChangeComposerFontSizePx?(value: number): void;
  onChangeBufferAssistantOutput?(value: boolean): void;
  onChangeNarrationEnabled(value: boolean): void;
  onChangeReplySoundEnabled(value: boolean): void;
  onChangeThemeMode(value: ThemeMode): void;
  onChangeVoiceApiKey(value: string): void;
  onChangeVoiceBaseUrl(value: string): void;
  onClose(): void;
  onSave(): void;
  onTestProvider(): void;
  onTriggerTestPush(): void;
}

export function VoiceSettingsDialog({
  audioMeta,
  enterToSendDraft,
  conversationFontSizePxDraft = 16,
  composerFontSizePxDraft = 16,
  bufferAssistantOutputDraft = true,
  narrationEnabledDraft,
  open,
  replySoundEnabled,
  status,
  themeMode,
  transportStatus,
  voiceApiKeyDraft,
  voiceBaseUrlDraft,
  onChangeEnterToSend,
  onChangeConversationFontSizePx,
  onChangeComposerFontSizePx,
  onChangeBufferAssistantOutput,
  onChangeNarrationEnabled,
  onChangeReplySoundEnabled,
  onChangeThemeMode,
  onChangeVoiceApiKey,
  onChangeVoiceBaseUrl,
  onClose,
  onSave,
  onTestProvider,
  onTriggerTestPush,
}: VoiceSettingsDialogProps) {
  if (!open) {
    return null;
  }

  return (
    <div className="dialogBackdrop" onClick={onClose}>
      <section className="dialogCard legacyDialog voiceSettingsDialog" onClick={(event) => event.stopPropagation()}>
        <header className="dialogHeader">
          <h2>Settings</h2>
          <p>Configure announcements and notification delivery.</p>
        </header>
        <div className="newSessionForm">
          {status ? <p className="fieldHint voiceSettingsStatus">{status}</p> : null}
          <Tabs defaultValue="provider" className="gap-4">
            <TabsList className="grid h-auto w-full grid-cols-2 gap-1 sm:grid-cols-4">
              <TabsTrigger value="provider">Provider</TabsTrigger>
              <TabsTrigger value="behavior">Behavior</TabsTrigger>
              <TabsTrigger value="display">Display</TabsTrigger>
              <TabsTrigger value="status">Status</TabsTrigger>
            </TabsList>

            <TabsContent value="provider" className="space-y-4">
              <label className="fieldBlock">
                <span className="fieldLabel">OpenAI-compatible API base URL</span>
                <input
                  value={voiceBaseUrlDraft}
                  onInput={(event) => onChangeVoiceBaseUrl(event.currentTarget.value)}
                  onChange={(event) => onChangeVoiceBaseUrl(event.currentTarget.value)}
                  placeholder="https://api.openai.com/v1"
                />
              </label>
              <label className="fieldBlock">
                <span className="fieldLabel">OpenAI-compatible API key</span>
                <input
                  value={voiceApiKeyDraft}
                  onInput={(event) => onChangeVoiceApiKey(event.currentTarget.value)}
                  onChange={(event) => onChangeVoiceApiKey(event.currentTarget.value)}
                  placeholder="sk-..."
                  type="password"
                />
              </label>
              <div className="formActions">
                <Button type="button" variant="outline" onClick={onTestProvider}>Test Provider</Button>
              </div>
            </TabsContent>

            <TabsContent value="behavior" className="space-y-4">
              <div className="fieldBlock toggleField">
                <span className="fieldLabel">Announcements</span>
                <label className="checkField">
                  <input
                    type="checkbox"
                    checked={narrationEnabledDraft}
                    onChange={(event) => onChangeNarrationEnabled(event.currentTarget.checked)}
                  />
                  <span>Announce narration messages</span>
                </label>
              </div>
              <div className="fieldBlock toggleField">
                <span className="fieldLabel">Composer</span>
                <label className="checkField">
                  <input
                    type="checkbox"
                    checked={enterToSendDraft}
                    onChange={(event) => onChangeEnterToSend(event.currentTarget.checked)}
                  />
                  <span>Press Enter to send</span>
                </label>
              </div>
              <div className="fieldBlock toggleField">
                <span className="fieldLabel">Reply sound</span>
                <label className="checkField">
                  <input
                    type="checkbox"
                    checked={replySoundEnabled}
                    onChange={(event) => onChangeReplySoundEnabled(event.currentTarget.checked)}
                  />
                  <span>Play a short beep when the assistant finishes a reply</span>
                </label>
              </div>
              <div className="fieldBlock toggleField">
                <span className="fieldLabel">Assistant output</span>
                <label className="checkField">
                  <input
                    type="checkbox"
                    checked={bufferAssistantOutputDraft}
                    onChange={(event) => onChangeBufferAssistantOutput?.(event.currentTarget.checked)}
                  />
                  <span>Buffer assistant output until the final message</span>
                </label>
              </div>
            </TabsContent>

            <TabsContent value="display" className="space-y-4">
              <div className="fieldBlock">
                <span className="fieldLabel">Text size</span>
                <div className="fieldGrid twoCol">
                  <label className="fieldBlock">
                    <span className="fieldHint">Conversation {Math.round(conversationFontSizePxDraft)}px</span>
                    <input
                      type="range"
                      min="12"
                      max="24"
                      step="1"
                      value={Math.round(conversationFontSizePxDraft)}
                      onInput={(event) => onChangeConversationFontSizePx?.(Number(event.currentTarget.value))}
                      onChange={(event) => onChangeConversationFontSizePx?.(Number(event.currentTarget.value))}
                    />
                  </label>
                  <label className="fieldBlock">
                    <span className="fieldHint">Input {Math.round(composerFontSizePxDraft)}px</span>
                    <input
                      type="range"
                      min="12"
                      max="24"
                      step="1"
                      value={Math.round(composerFontSizePxDraft)}
                      onInput={(event) => onChangeComposerFontSizePx?.(Number(event.currentTarget.value))}
                      onChange={(event) => onChangeComposerFontSizePx?.(Number(event.currentTarget.value))}
                    />
                  </label>
                </div>
              </div>
              <div className="fieldBlock">
                <span className="fieldLabel">Theme</span>
                <div className="fieldGrid twoCol">
                  <label className="toggleOption flex cursor-pointer items-start gap-3 rounded-2xl border border-border/70 bg-background/80 px-3 py-3 text-sm">
                    <input
                      type="radio"
                      name="theme-mode"
                      checked={themeMode === "light"}
                      onChange={() => onChangeThemeMode("light")}
                    />
                    <span className="space-y-1">
                      <span className="block font-medium text-foreground">Light</span>
                      <span className="block text-muted-foreground">Paper-like surfaces with cobalt markdown accents.</span>
                    </span>
                  </label>
                  <label className="toggleOption flex cursor-pointer items-start gap-3 rounded-2xl border border-border/70 bg-background/80 px-3 py-3 text-sm">
                    <input
                      type="radio"
                      name="theme-mode"
                      checked={themeMode === "dark"}
                      onChange={() => onChangeThemeMode("dark")}
                    />
                    <span className="space-y-1">
                      <span className="block font-medium text-foreground">Dark</span>
                      <span className="block text-muted-foreground">Ink surfaces with brighter markdown contrast for long sessions.</span>
                    </span>
                  </label>
                </div>
              </div>
            </TabsContent>

            <TabsContent value="status" className="space-y-4">
              <div className="voiceSettingsMeta fieldHint">
                <span>Realtime transport: {transportStatus?.active === "connect" ? "ConnectRPC experimental" : "WebSocket"}</span>
                <span>Connect opt-in: {transportStatus?.connectOptIn ? "on" : "off"}</span>
                <span>Connect capability: {transportStatus?.connectAvailable ? "available" : "unavailable"}</span>
                <span>Desktop eligible: {transportStatus?.desktopEligible ? "yes" : "no"}</span>
                <span>Connect path: {transportStatus?.connectPath || "/api/connect"}</span>
                <span>Listeners: {audioMeta.listeners}</span>
                <span>Queue: {audioMeta.queue}</span>
                <span>Segments: {audioMeta.segments}</span>
                <span>Mobile notifications: {audioMeta.enabledDevices}/{audioMeta.totalDevices}</span>
              </div>
              {audioMeta.lastError ? (
                <p className="fieldHint voiceSettingsStatus">Audio error: {audioMeta.lastError}</p>
              ) : null}
            </TabsContent>
          </Tabs>

          <div className="formActions dialogFormActions">
            <Button type="button" variant="outline" onClick={onTriggerTestPush}>Test Push</Button>
            <div className="flex-1" />
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="button" onClick={onSave}>Save</Button>
          </div>
        </div>
      </section>
    </div>
  );
}
