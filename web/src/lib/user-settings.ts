export interface UserDisplaySettings {
  conversationFontSizePx: number;
  composerFontSizePx: number;
  bufferAssistantOutput: boolean;
}

export const DEFAULT_USER_DISPLAY_SETTINGS: UserDisplaySettings = {
  conversationFontSizePx: 16,
  composerFontSizePx: 16,
  bufferAssistantOutput: true,
};

const STORAGE_KEY = "actrail.userDisplaySettings.v1";

function clampFontSizePx(value: unknown, fallback: number): number {
  const numeric = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(numeric)) {
    return fallback;
  }
  return Math.min(24, Math.max(12, Math.round(numeric)));
}

function scaleToPx(value: unknown, fallback: number): number {
  const numeric = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(numeric)) {
    return fallback;
  }
  return clampFontSizePx(numeric * 16, fallback);
}

export function readUserDisplaySettings(): UserDisplaySettings {
  if (typeof window === "undefined") {
    return DEFAULT_USER_DISPLAY_SETTINGS;
  }
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) {
      return DEFAULT_USER_DISPLAY_SETTINGS;
    }
    const parsed = JSON.parse(raw) as Partial<UserDisplaySettings> & {
      conversationFontScale?: unknown;
      composerFontScale?: unknown;
      bufferAssistantOutput?: unknown;
    };
    return {
      conversationFontSizePx: clampFontSizePx(
        parsed.conversationFontSizePx ?? scaleToPx(parsed.conversationFontScale, DEFAULT_USER_DISPLAY_SETTINGS.conversationFontSizePx),
        DEFAULT_USER_DISPLAY_SETTINGS.conversationFontSizePx,
      ),
      composerFontSizePx: clampFontSizePx(
        parsed.composerFontSizePx ?? scaleToPx(parsed.composerFontScale, DEFAULT_USER_DISPLAY_SETTINGS.composerFontSizePx),
        DEFAULT_USER_DISPLAY_SETTINGS.composerFontSizePx,
      ),
      bufferAssistantOutput: typeof parsed.bufferAssistantOutput === "boolean" ? parsed.bufferAssistantOutput : DEFAULT_USER_DISPLAY_SETTINGS.bufferAssistantOutput,
    };
  } catch {
    return DEFAULT_USER_DISPLAY_SETTINGS;
  }
}

export function writeUserDisplaySettings(settings: UserDisplaySettings) {
  if (typeof window === "undefined") {
    return;
  }
  const normalized = {
    conversationFontSizePx: clampFontSizePx(settings.conversationFontSizePx, DEFAULT_USER_DISPLAY_SETTINGS.conversationFontSizePx),
    composerFontSizePx: clampFontSizePx(settings.composerFontSizePx, DEFAULT_USER_DISPLAY_SETTINGS.composerFontSizePx),
    bufferAssistantOutput: settings.bufferAssistantOutput !== false,
  };
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(normalized));
}

export function applyUserDisplaySettings(settings: UserDisplaySettings) {
  if (typeof document === "undefined") {
    return;
  }
  const root = document.documentElement;
  root.style.setProperty("--conversation-font-size", `${clampFontSizePx(settings.conversationFontSizePx, 16)}px`);
  root.style.setProperty("--composer-font-size", `${clampFontSizePx(settings.composerFontSizePx, 16)}px`);
}
