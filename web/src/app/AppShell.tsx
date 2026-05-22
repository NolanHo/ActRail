import { lazy, Suspense } from "preact/compat";
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { api } from "../lib/api";
import { ConversationPane } from "../components/conversation/ConversationPane";
import { ConversationStateTray } from "../components/conversation/ConversationStateTray";
import { Composer } from "../components/composer/Composer";
import { SessionFileDetail, SessionFileRail, useSessionFileViewState } from "../components/session-files/SessionFileView";
import type { FileViewMode } from "../components/workspace/FileViewerDialog";
import { AppShellSidebar, GlobalNavRail, type DesktopGlobalView } from "./app-shell/AppShellSidebar";
import { AppShellToolbar, type ConversationStatusItem } from "./app-shell/AppShellToolbar";
import { AppShellWorkspaceOverlays } from "./app-shell/AppShellWorkspaceOverlays";
import { SessionRuntimeSettingsDialog } from "./app-shell/SessionRuntimeSettingsDialog";
import { SchedulerView } from "../components/scheduler/SchedulerView";
import { TeamsThreadView, useTeamsData } from "../components/teams/TeamsView";
import { AskUserView } from "../components/waits/AskUserView";
import { MobileShell } from "./app-shell/MobileShell";
import { VoiceSettingsDialog } from "./app-shell/VoiceSettingsDialog";
import { useAppShellAudio } from "./app-shell/useAppShellAudio";
import { useAppShellEvents } from "./app-shell/useAppShellEvents";
import { useAppShellNotifications } from "./app-shell/useAppShellNotifications";
import { useAppShellSessionEffects } from "./app-shell/useAppShellSessionEffects";
import { setConnectWireFormat } from "../domains/sessions/store";
import {
  shallowEqual,
  useLiveSessionStoreApi,
  useLiveSessionStoreSelector,
  useMessagesStoreSelector,
  useSessionUiStoreApi,
  useSessionUiStoreSelector,
  useSessionsStoreApi,
  useSessionsStoreSelector,
  useWaitsStoreApi,
  useWaitsStoreSelector,
} from "./providers";
import {
  applyThemeMode,
  MOBILE_LAYOUT_MEDIA_QUERY,
  readThemeMode,
  shouldUseMobileLayout,
  shortSessionId,
  writeThemeMode,
} from "./app-shell/utils";
import { getSessionRuntimeId } from "../lib/session-identity";
import { getSessionDisplayName } from "../lib/session-display";
import { applyUserDisplaySettings, readUserDisplaySettings, writeUserDisplaySettings } from "../lib/user-settings";
import { backendCapability, normalizeLaunchBackend } from "../lib/launch";
import { defaultModelFor, defaultProviderFor, defaultReasoningFor } from "../lib/launch-options";
import type { SwitchSessionModelResponse } from "../lib/types";

type WorkspaceTab = "metadata";
type FinalResponseSignature = {
  key: string;
  notificationText: string;
  sessionId: string;
};

const SIDEBAR_WIDTH_STORAGE_KEY = "actrail.sidebarWidthPx.v1";
const SIDEBAR_WIDTH_DEFAULT_PX = 320;
const SIDEBAR_WIDTH_MIN_PX = 288;
const SIDEBAR_WIDTH_MAX_PX = 480;
const SIDEBAR_WIDTH_STEP_PX = 16;
const DEFAULT_CODEX_SESSION_CWD = "/root/docs";

function clampSidebarWidth(value: number) {
  if (!Number.isFinite(value)) {
    return SIDEBAR_WIDTH_DEFAULT_PX;
  }
  return Math.min(SIDEBAR_WIDTH_MAX_PX, Math.max(SIDEBAR_WIDTH_MIN_PX, Math.round(value)));
}

function readSidebarWidthPx() {
  if (typeof window === "undefined") {
    return SIDEBAR_WIDTH_DEFAULT_PX;
  }
  const raw = window.localStorage.getItem(SIDEBAR_WIDTH_STORAGE_KEY);
  if (!raw) {
    return SIDEBAR_WIDTH_DEFAULT_PX;
  }
  return clampSidebarWidth(Number(raw));
}

function writeSidebarWidthPx(value: number) {
  if (typeof window === "undefined") {
    return;
  }
  window.localStorage.setItem(SIDEBAR_WIDTH_STORAGE_KEY, String(clampSidebarWidth(value)));
}

function formatTokenK(value: number) {
  const normalized = Math.max(0, Math.round(value));
  if (normalized <= 0) {
    return "0";
  }
  return `${Math.round(normalized / 1000)}K`;
}

function formatIodStart(ts: number | undefined) {
  if (typeof ts !== "number" || !Number.isFinite(ts) || ts <= 0) {
    return "";
  }
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(ts * 1000));
}

function contextUsageStatusLabel(usage: { used_tokens?: number; total_tokens?: number; percent_used?: number } | null | undefined) {
  if (!usage) {
    return "";
  }
  const usedTokens = typeof usage.used_tokens === "number" && Number.isFinite(usage.used_tokens)
    ? Math.max(0, Math.round(usage.used_tokens))
    : null;
  const totalTokens = typeof usage.total_tokens === "number" && Number.isFinite(usage.total_tokens)
    ? Math.max(0, Math.round(usage.total_tokens))
    : null;
  const percentUsed = typeof usage.percent_used === "number" && Number.isFinite(usage.percent_used)
    ? Math.round(usage.percent_used)
    : null;
  if (usedTokens === null) {
    return "";
  }
  if (totalTokens === null || totalTokens <= 0 || percentUsed === null) {
    return `${formatTokenK(usedTokens)} used`;
  }
  return `${formatTokenK(usedTokens)}/${formatTokenK(totalTokens)} ${percentUsed}%`;
}

function finalResponseSignatureForEvent(sessionId: string, event: Record<string, unknown>): FinalResponseSignature | null {
  const messageId = typeof event.message_id === "string" ? event.message_id : "";
  const eventId = typeof event.event_id === "string" ? event.event_id : "";
  const seq = typeof event.seq === "number" || typeof event.seq === "string" ? event.seq : "";
  const ts = typeof event.ts === "number" || typeof event.ts === "string" ? event.ts : "";
  const notificationText = typeof event.notification_text === "string"
    ? event.notification_text
    : typeof event.text === "string"
      ? event.text
      : "";
  if (!messageId && !(eventId || seq) && !notificationText.trim()) {
    return null;
  }
  const key = [
    sessionId,
    messageId,
    eventId || seq,
    ts,
    notificationText,
  ].join("\u0001");
  return key.trim() ? { key, notificationText, sessionId } : null;
}

const LazySessionWorkspace = lazy(() => import("../components/workspace/SessionWorkspace").then((module) => ({ default: module.SessionWorkspace })));

function WorkspaceLoadingFallback() {
  return <div className="rounded-2xl border border-border/60 bg-muted/20 p-5 text-sm text-muted-foreground">Loading details...</div>;
}

function EmptyDetailsWorkspace() {
  return (
    <aside className="workspacePane">
      <section className="workspaceSection">
        <h3>Diagnostics</h3>
        <p>No diagnostics available.</p>
      </section>
      <section className="workspaceSection">
        <h3>Inbox</h3>
        <ul className="workspaceList">
          <li>No inbox items</li>
        </ul>
      </section>
      <section className="workspaceSection">
        <h3>Files</h3>
        <ul className="workspaceList">
          <li>No tracked files</li>
        </ul>
      </section>
      <section className="workspaceSection">
        <h3>UI Requests</h3>
        <p>No pending requests</p>
      </section>
    </aside>
  );
}

export function AppShell() {
  const { activeSessionId, bootstrapCapabilities, bootstrapLoaded, items, newSessionDefaults, realtimeTransport } = useSessionsStoreSelector((state) => ({
    activeSessionId: state.activeSessionId,
    bootstrapCapabilities: state.bootstrapCapabilities,
    bootstrapLoaded: state.bootstrapLoaded,
    items: state.items,
    newSessionDefaults: state.newSessionDefaults,
    realtimeTransport: state.realtimeTransport,
  }), shallowEqual);
  const liveActiveSessionState = useLiveSessionStoreSelector((state) => {
    if (!activeSessionId) {
      return {
        busy: undefined,
        contextUsage: null,
        generating: undefined,
        hasBusy: false,
        runtimeState: undefined,
        transport: undefined,
      };
    }
    const busyBySessionId = state.busyBySessionId ?? {};
    const contextUsageBySessionId = state.contextUsageBySessionId ?? {};
    const generatingBySessionId = state.generatingBySessionId ?? {};
    const runtimeStateBySessionId = state.runtimeStateBySessionId ?? {};
    const transportBySessionId = state.transportBySessionId ?? {};
    return {
      busy: busyBySessionId[activeSessionId],
      contextUsage: contextUsageBySessionId[activeSessionId] ?? null,
      generating: generatingBySessionId[activeSessionId],
      hasBusy: Object.prototype.hasOwnProperty.call(busyBySessionId, activeSessionId),
      runtimeState: runtimeStateBySessionId[activeSessionId],
      transport: transportBySessionId[activeSessionId],
    };
  }, shallowEqual);
  const monitoredFinalResponseSignatures = useMessagesStoreSelector((state) => {
    const monitoredSessionIds = new Set<string>();
    for (const session of items) {
      const sessionId = typeof session.session_id === "string" ? session.session_id : "";
      if (!sessionId) {
        continue;
      }
      if (sessionId === activeSessionId || session.busy === true) {
        monitoredSessionIds.add(sessionId);
      }
    }
    const signatures: FinalResponseSignature[] = [];
    for (const sessionId of monitoredSessionIds) {
      const events = state.bySessionId[sessionId] ?? [];
      for (let index = events.length - 1; index >= 0; index -= 1) {
        const event = events[index];
        if (event?.role !== "assistant" || event.pending === true || event.message_class !== "final_response") {
          continue;
        }
        const signature = finalResponseSignatureForEvent(sessionId, event);
        if (signature) {
          signatures.push(signature);
        }
        break;
      }
    }
    return signatures;
  }, (left, right) => left.length === right.length && left.every((item, index) => item.key === right[index]?.key));
  const sessionUiSessionId = useSessionUiStoreSelector((state) => state.sessionId);
  const activeWait = useWaitsStoreSelector((state) => activeSessionId ? state.activeBySessionId[activeSessionId] ?? null : null);
  const sessionsStoreApi = useSessionsStoreApi();
  const liveSessionStoreApi = useLiveSessionStoreApi();
  const sessionUiStoreApi = useSessionUiStoreApi();
  const waitsStoreApi = useWaitsStoreApi();
  const [newSessionOpen, setNewSessionOpen] = useState(false);
  const [workspaceOpen, setWorkspaceOpen] = useState(false);
  const [fileViewerOpen, setFileViewerOpen] = useState(false);
  const [inboxOpen, setInboxOpen] = useState(false);
  const [runtimeSettingsOpen, setRuntimeSettingsOpen] = useState(false);
  const [workspaceInitialTab, setWorkspaceInitialTab] = useState<WorkspaceTab>("metadata");
  const [fileViewerPath, setFileViewerPath] = useState("");
  const [fileViewerLine, setFileViewerLine] = useState<number | null>(null);
  const [fileViewerMode, setFileViewerMode] = useState<FileViewMode | null>(null);
  const [fileViewerRequestKey, setFileViewerRequestKey] = useState(0);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [sidebarWidthPx, setSidebarWidthPx] = useState(readSidebarWidthPx);
  const [sidebarResizing, setSidebarResizing] = useState(false);
  const [desktopGlobalView, setDesktopGlobalView] = useState<DesktopGlobalView>("sessions");
  const [selectedTeamId, setSelectedTeamId] = useState("");
  const teamsData = useTeamsData(desktopGlobalView === "teams" ? 5000 : 0);
  const [themeMode, setThemeMode] = useState(() => readThemeMode());
  const [displaySettings, setDisplaySettings] = useState(() => readUserDisplaySettings());
  const [displaySettingsDraft, setDisplaySettingsDraft] = useState(() => readUserDisplaySettings());
  const [realtimeConnected, setRealtimeConnected] = useState(false);
  const [runtimeProbePending, setRuntimeProbePending] = useState(false);
  const [supervisorProviderBaseUrlDraft, setSupervisorProviderBaseUrlDraft] = useState("");
  const [supervisorProviderModelDraft, setSupervisorProviderModelDraft] = useState("");
  const [supervisorProviderApiKeyDraft, setSupervisorProviderApiKeyDraft] = useState("");
  const [supervisorProviderStatus, setSupervisorProviderStatus] = useState("");
  const voiceSupported = bootstrapCapabilities?.voice !== false;
  const notificationsSupported = bootstrapCapabilities?.notifications !== false;
  const {
    announcementEnabled,
    announcementLabel,
    closeVoiceSettings,
    enterToSendDraft,
    liveAudioRef,
    narrationEnabledDraft,
    openVoiceSettings,
    saveVoiceSettings,
    testVoiceProvider,
    setEnterToSendDraft,
    setNarrationEnabledDraft,
    setVoiceSettingsStatus,
    setVoiceApiKeyDraft,
    setVoiceBaseUrlDraft,
    startAnnouncementPlayback,
    toggleAnnouncements,
    voiceApiKeyDraft,
    voiceBaseUrlDraft,
    voiceSettings,
    voiceSettingsOpen,
    voiceSettingsStatus,
  } = useAppShellAudio({ bootstrapLoaded, voiceSupported });
  const backgroundReplySoundPrimedSessionIdsRef = useRef(new Set<string>());
  const suppressedReplySoundSessionIdsRef = useRef(new Set<string>());
  const activeSessionReplySoundPrimingRef = useRef<string | null>(null);
  const sidebarResizeRef = useRef<{ startX: number; startWidth: number } | null>(null);
  const sidebarWidthPxRef = useRef(sidebarWidthPx);

  useEffect(() => {
    applyUserDisplaySettings(displaySettings);
    (liveSessionStoreApi as { setBufferAssistantOutput?: (value: boolean) => void }).setBufferAssistantOutput?.(displaySettings.bufferAssistantOutput);
  }, [displaySettings, liveSessionStoreApi]);

  useEffect(() => {
    if (!voiceSettingsOpen) {
      return;
    }
    let cancelled = false;
    setDisplaySettingsDraft(displaySettings);
    setSupervisorProviderStatus("Loading supervisor provider...");
    api.getSupervisorProvider()
      .then((provider) => {
        if (cancelled) return;
        setSupervisorProviderBaseUrlDraft(provider.base_url || "");
        setSupervisorProviderModelDraft(provider.model || "");
        setSupervisorProviderApiKeyDraft("");
        setSupervisorProviderStatus(provider.api_key_configured ? "Supervisor API key configured" : "Supervisor API key missing");
      })
      .catch((error) => {
        if (cancelled) return;
        setSupervisorProviderStatus(error instanceof Error ? error.message : "Unable to load supervisor provider");
      });
    return () => {
      cancelled = true;
    };
  }, [displaySettings, voiceSettingsOpen]);

  const saveSettings = async () => {
    setDisplaySettings(displaySettingsDraft);
    writeUserDisplaySettings(displaySettingsDraft);
    const saveVoice = saveVoiceSettings();
    setSupervisorProviderStatus("Saving supervisor provider...");
    try {
      const provider = await api.saveSupervisorProvider({
        base_url: supervisorProviderBaseUrlDraft,
        model: supervisorProviderModelDraft,
        ...(supervisorProviderApiKeyDraft.trim() ? { api_key: supervisorProviderApiKeyDraft.trim() } : {}),
      });
      setSupervisorProviderBaseUrlDraft(provider.base_url || "");
      setSupervisorProviderModelDraft(provider.model || "");
      setSupervisorProviderApiKeyDraft("");
      setSupervisorProviderStatus(provider.api_key_configured ? "Supervisor API key configured" : "Supervisor API key missing");
    } catch (error) {
      setSupervisorProviderStatus(error instanceof Error ? error.message : "Unable to save supervisor provider");
    }
    await saveVoice;
  };
  useEffect(() => {
    if (!activeSessionId) return;
    suppressedReplySoundSessionIdsRef.current.add(activeSessionId);
    activeSessionReplySoundPrimingRef.current = activeSessionId;
  }, [activeSessionId]);

  useEffect(() => {
    sessionsStoreApi.refreshBootstrap().catch(() => undefined);
  }, [sessionsStoreApi]);

  const activeSession = items.find((session) => session.session_id === activeSessionId) ?? null;
  const activeBackendCapabilities = backendCapability(newSessionDefaults, activeSession?.agent_backend);
  const activeSessionRuntimeId = getSessionRuntimeId(activeSession);
  const activeSessionPending = activeSession?.pending_startup === true;
  const listedRuntimeState = typeof activeSession?.runtime_state === "string" ? activeSession.runtime_state.trim() : "";
  const liveRuntimeState = typeof liveActiveSessionState.runtimeState === "string" ? liveActiveSessionState.runtimeState.trim() : "";
  const activeRuntimeState = listedRuntimeState === "failed" || listedRuntimeState === "ended"
    ? listedRuntimeState
    : liveRuntimeState || listedRuntimeState;
  const liveTransport = liveActiveSessionState.transport ?? null;
  const liveTransportState = typeof liveTransport?.state === "string" ? liveTransport.state.trim() : "";
  const liveTransportResetRequired = liveTransport?.reset_required === true;
  const listedTransportState = typeof activeSession?.transport_state === "string" ? activeSession.transport_state.trim() : "";
  const activeTransportState = liveTransportState || listedTransportState;
  const activeTransportResetRequired = liveTransportState
    ? liveTransportResetRequired
    : activeSession?.reset_required === true;
  const activeSessionTransportUnavailable = activeTransportResetRequired || ["broken", "failed", "ended"].includes(activeTransportState);
  const activeSessionTerminalRuntime = activeRuntimeState === "failed" || activeRuntimeState === "ended";
  const activeSessionHasLiveBusy = Boolean(activeSessionId && liveActiveSessionState.hasBusy);
  const activeSessionLiveBusy = Boolean(activeSessionId && !activeSessionTransportUnavailable && !activeSessionTerminalRuntime && liveActiveSessionState.busy === true);
  const visibleActiveWait = activeWait ?? activeSession?.active_wait ?? null;
  const activeSessionBusy = Boolean(
    !activeSessionTransportUnavailable
      && !activeSessionTerminalRuntime
      && (activeSessionHasLiveBusy ? activeSessionLiveBusy : activeSession?.busy === true),
  );
  const activeSessionGenerating = Boolean(activeSessionBusy && liveActiveSessionState.generating === true);
  const activeTitle = activeSession
    ? getSessionDisplayName(activeSession, shortSessionId(activeSession.session_id))
    : "No session selected";
  const activeCodexSessionCwd = activeSession?.cwd || DEFAULT_CODEX_SESSION_CWD;
  const activeBackend = normalizeLaunchBackend(activeSession?.agent_backend);
  const activeBackendDefaults = newSessionDefaults?.backends?.[activeBackend] || {};
  const activeProviderChoice = typeof activeSession?.provider_choice === "string" ? activeSession.provider_choice.trim() : "";
  const activeDefaultProvider = activeProviderChoice || defaultProviderFor(activeBackendDefaults);
  const activeModel = activeSession
    ? (typeof activeSession.model === "string" ? activeSession.model.trim() : "") || defaultModelFor(activeBackendDefaults, activeBackend, activeDefaultProvider)
    : "";
  const activeReasoningEffort = activeSession
    ? (typeof activeSession.reasoning_effort === "string" ? activeSession.reasoning_effort.trim() : "") || defaultReasoningFor(activeBackendDefaults, activeBackend)
    : "";
  const activeContextUsageLabel = activeSessionId ? contextUsageStatusLabel(liveActiveSessionState.contextUsage) : "";
  const activeQueueCount = typeof activeSession?.queue_len === "number" && Number.isFinite(activeSession.queue_len)
    ? Math.max(0, Math.round(activeSession.queue_len))
    : 0;
  const conversationStatusItems = useMemo<ConversationStatusItem[]>(() => {
    if (!activeSession) {
      return [];
    }
    const items: ConversationStatusItem[] = [];
    if (activeTransportResetRequired || activeTransportState === "broken") {
      items.push({ label: "Runtime", value: "broken", tone: "error" });
    } else if (activeTransportState === "failed") {
      items.push({ label: "Runtime", value: "failed", tone: "error" });
    } else if (activeTransportState === "ended") {
      items.push({ label: "Runtime", value: "ended", tone: "error" });
    } else if (activeTransportState === "silent" || activeTransportState === "stalled") {
      items.push({ label: "Runtime", value: activeTransportState, tone: "error" });
    } else if (activeRuntimeState === "failed" || activeRuntimeState === "ended") {
      items.push({ label: "Runtime", value: activeRuntimeState, tone: "error" });
    } else if (activeSessionGenerating) {
      items.push({ label: "Runtime", value: "generating", tone: "busy" });
    } else if (activeSessionBusy) {
      items.push({ label: "Runtime", value: activeRuntimeState || "busy", tone: "busy" });
    } else if (activeSession.pending_startup === true) {
      items.push({ label: "Runtime", value: "starting", tone: "attention" });
    } else {
      items.push({ label: "Runtime", value: activeRuntimeState || "idle", tone: "success" });
    }
    const transportState = activeTransportState;
    if (transportState) {
      const transportTone: ConversationStatusItem["tone"] = activeTransportResetRequired || ["broken", "failed", "ended", "silent", "stalled"].includes(transportState)
        ? "error"
        : transportState === "starting"
          ? "attention"
          : "default";
      items.push({ label: "Transport", value: transportState, tone: transportTone });
    }
    if (activeContextUsageLabel) {
      items.push({ label: "Context", value: activeContextUsageLabel });
    }
    if (activeSession.agent_backend) {
      const mode = typeof activeSession.iod?.mode === "string" ? activeSession.iod.mode.trim() : "";
      const backend = mode ? `${activeSession.agent_backend}/${mode}` : activeSession.agent_backend;
      items.push({ label: "Backend", value: backend });
    }
    if (activeModel) {
      items.push({ label: "Model", value: activeModel, actionLabel: "Change runtime model", onActivate: () => setRuntimeSettingsOpen(true) });
    }
    if (activeReasoningEffort) {
      items.push({ label: "Effort", value: activeReasoningEffort, actionLabel: "Change reasoning effort", onActivate: () => setRuntimeSettingsOpen(true) });
    }
    if (activeSession.iod) {
      const mode = typeof activeSession.iod.mode === "string" ? activeSession.iod.mode.trim() : "";
      const version = [activeSession.iod.git_sha, activeSession.iod.build_date, mode].filter(Boolean).join(" ");
      if (version) {
        items.push({ label: "iod", value: version });
      }
      const started = formatIodStart(activeSession.iod.start_ts);
      if (started) {
        items.push({ label: "Pi started", value: started });
      }
    }
    const supervisor = activeSession.supervisor;
    if (supervisor?.enabled) {
      const value = supervisor.status === "limit_reached"
        ? `limit ${supervisor.consecutive_injections}/${supervisor.max_consecutive_injections}`
        : `on ${supervisor.consecutive_injections}/${supervisor.max_consecutive_injections}`;
      items.push({ label: "Supervisor", value, tone: supervisor.status === "limit_reached" ? "error" : "attention" });
    }
    if (activeQueueCount > 0) {
      items.push({ label: "Inbox", value: String(activeQueueCount), tone: "attention" });
    }
    if (visibleActiveWait) {
      items.push({ label: "Wait", value: "user input", tone: "attention" });
    }
    return items;
  }, [activeContextUsageLabel, activeModel, activeQueueCount, activeReasoningEffort, activeRuntimeState, activeSession, activeSessionBusy, activeSessionGenerating, visibleActiveWait]);

  const playReplyBeep = () => {
    try {
      const AudioContextCtor = (window as any).AudioContext
        || (window as any).webkitAudioContext
        || (globalThis as any).AudioContext
        || (globalThis as any).webkitAudioContext;
      if (!AudioContextCtor) {
        return;
      }
      const ctx = new AudioContextCtor();
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.connect(gain);
      gain.connect(ctx.destination);
      osc.type = "triangle";
      osc.frequency.setValueAtTime(987.77, ctx.currentTime);
      gain.gain.setValueAtTime(0.0001, ctx.currentTime);
      gain.gain.exponentialRampToValueAtTime(0.08, ctx.currentTime + 0.01);
      gain.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + 0.18);
      osc.start(ctx.currentTime);
      osc.stop(ctx.currentTime + 0.18);
      osc.onended = () => {
        void ctx.close().catch(() => undefined);
      };
    } catch {
      // Best-effort local cue only.
    }
  };

  const {
    notificationLabel,
    notificationsEnabled,
    refreshNotificationFeed,
    replySoundEnabled,
    setReplySoundEnabled,
    showRealtimeNotification,
    toggleNotifications,
  } = useAppShellNotifications({
    activeSessionId,
    activeTitle,
    bootstrapLoaded,
    finalResponseSignatures: monitoredFinalResponseSignatures,
    notificationsSupported,
    playReplyBeep,
    realtimeConnected,
    suppressedReplySoundSessionIdsRef,
    voiceSettings,
  });

  useAppShellEvents({
    activeSessionBackend: activeSession?.agent_backend,
    activeSessionHistorical: activeSession?.historical === true,
    activeSessionId,
    activeSessionPending,
    activeSessionRuntimeId,
    bootstrapLoaded,
    items,
    liveSessionStoreApi,
    onConnectionChange: setRealtimeConnected,
    refreshNotificationsFeed: refreshNotificationFeed,
    showRealtimeNotification,
    sessionUiStoreApi,
    sessionsStoreApi,
    waitsStoreApi,
    workspaceOpen,
    bufferAssistantOutput: displaySettings.bufferAssistantOutput,
  });

  useAppShellSessionEffects({
    activeSessionBackend: activeSession?.agent_backend,
    activeSessionHistorical: activeSession?.historical === true,
    activeSessionPending,
    activeSessionId,
    activeSessionRuntimeId,
    activeSessionLiveBusy,
    backgroundReplySoundPrimedSessionIdsRef,
    items,
    liveSessionStoreApi,
    realtimeConnected,
    replySoundEnabled,
    sessionUiStoreApi,
    sessionsStoreApi,
    workspaceOpen,
    activeSessionReplySoundPrimingRef,
    suppressedReplySoundSessionIdsRef,
  });

  const sessionUiMatchesActiveSession = !!activeSessionId && sessionUiSessionId === activeSessionId;
  const showInterruptAction = !activeSessionId || activeSessionBusy;
  const [mobileLayout, setMobileLayout] = useState(() => shouldUseMobileLayout());

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }

    const mediaQuery = window.matchMedia(MOBILE_LAYOUT_MEDIA_QUERY);
    const update = () => {
      setMobileLayout(mediaQuery.matches);
    };

    update();
    if (typeof mediaQuery.addEventListener === "function") {
      mediaQuery.addEventListener("change", update);
    } else {
      mediaQuery.addListener?.(update);
    }

    return () => {
      if (typeof mediaQuery.removeEventListener === "function") {
        mediaQuery.removeEventListener("change", update);
      } else {
        mediaQuery.removeListener?.(update);
      }
    };
  }, []);

  const openFileViewer = (path = "", line: number | null = null, mode: FileViewMode | null = null) => {
    setFileViewerPath(path);
    setFileViewerLine(line);
    setFileViewerMode(mode);
    setFileViewerRequestKey((current) => current + 1);
    setFileViewerOpen(true);
  };

  const closeFileViewer = () => {
    setFileViewerOpen(false);
    setFileViewerPath("");
    setFileViewerLine(null);
    setFileViewerMode(null);
  };

  const logout = async () => {
    try {
      await api.logout();
      if (typeof window !== "undefined") {
        window.location.reload();
      }
    } catch {
      // allow retry from the UI
    }
  };

  useEffect(() => {
    applyThemeMode(themeMode);
    writeThemeMode(themeMode);
  }, [themeMode]);

  useEffect(() => {
    if (activeSessionId) {
      setSidebarOpen(false);
    }
  }, [activeSessionId]);

  useEffect(() => {
    setFileViewerOpen(false);
    setInboxOpen(false);
    setRuntimeSettingsOpen(false);
    setWorkspaceInitialTab("metadata");
  }, [activeSessionId]);

  useEffect(() => {
    sidebarWidthPxRef.current = sidebarWidthPx;
  }, [sidebarWidthPx]);

  const shellClassName = useMemo(
    () => ["appShell", "editorialShell", "withGlobalNav", mobileLayout ? "isMobileLayout" : "", sidebarResizing ? "isResizingSidebar" : ""].filter(Boolean).join(" "),
    [mobileLayout, sidebarResizing],
  );
  const shellStyle = useMemo(() => ({ "--sidebar-w": `${sidebarWidthPx}px` }), [sidebarWidthPx]);

  const commitSidebarWidth = useCallback((value: number) => {
    const next = clampSidebarWidth(value);
    setSidebarWidthPx(next);
    writeSidebarWidthPx(next);
  }, []);

  const beginSidebarResize = useCallback((event: any) => {
    if (event.button !== 0) {
      return;
    }
    event.preventDefault();
    sidebarResizeRef.current = { startX: event.clientX, startWidth: sidebarWidthPxRef.current };
    setSidebarResizing(true);
  }, []);

  useEffect(() => {
    if (!sidebarResizing) {
      return;
    }

    const handleResizeMove = (event: MouseEvent | PointerEvent) => {
      const resize = sidebarResizeRef.current;
      if (!resize) {
        return;
      }
      const next = clampSidebarWidth(resize.startWidth + event.clientX - resize.startX);
      sidebarWidthPxRef.current = next;
      setSidebarWidthPx(next);
    };

    const finishResize = () => {
      sidebarResizeRef.current = null;
      setSidebarResizing(false);
      writeSidebarWidthPx(sidebarWidthPxRef.current);
    };

    window.addEventListener("pointermove", handleResizeMove);
    window.addEventListener("pointerup", finishResize);
    window.addEventListener("pointercancel", finishResize);
    window.addEventListener("mousemove", handleResizeMove);
    window.addEventListener("mouseup", finishResize);
    return () => {
      window.removeEventListener("pointermove", handleResizeMove);
      window.removeEventListener("pointerup", finishResize);
      window.removeEventListener("pointercancel", finishResize);
      window.removeEventListener("mousemove", handleResizeMove);
      window.removeEventListener("mouseup", finishResize);
    };
  }, [sidebarResizing]);

  const handleSidebarResizeKeyDown = useCallback((event: any) => {
    if (event.key === "ArrowLeft") {
      event.preventDefault();
      commitSidebarWidth(sidebarWidthPxRef.current - SIDEBAR_WIDTH_STEP_PX);
    } else if (event.key === "ArrowRight") {
      event.preventDefault();
      commitSidebarWidth(sidebarWidthPxRef.current + SIDEBAR_WIDTH_STEP_PX);
    } else if (event.key === "Home") {
      event.preventDefault();
      commitSidebarWidth(SIDEBAR_WIDTH_MIN_PX);
    } else if (event.key === "End") {
      event.preventDefault();
      commitSidebarWidth(SIDEBAR_WIDTH_MAX_PX);
    }
  }, [commitSidebarWidth]);

  const renderWorkspaceDetails = () => (
    sessionUiMatchesActiveSession || visibleActiveWait ? (
      <Suspense fallback={<WorkspaceLoadingFallback />}>
        <LazySessionWorkspace mode="details" initialTab={workspaceInitialTab} />
      </Suspense>
    ) : <EmptyDetailsWorkspace />
  );

  const openWorkspace = (initialTab: WorkspaceTab = "metadata") => {
    setWorkspaceInitialTab(initialTab);
    setWorkspaceOpen(true);
  };

  const probeActiveSessionState = async () => {
    if (!activeSessionId || runtimeProbePending) return;
    setRuntimeProbePending(true);
    try {
      if (activeSessionRuntimeId) {
        await liveSessionStoreApi.probe(activeSessionId, activeSessionRuntimeId);
      } else {
        await liveSessionStoreApi.probe(activeSessionId);
      }
      await sessionsStoreApi.refresh();
    } finally {
      setRuntimeProbePending(false);
    }
  };

  const interruptActiveSession = useCallback(async () => {
    if (!activeSessionId || !activeSessionBusy) return;
    if (activeSessionRuntimeId) {
      await api.interruptSession(activeSessionId, activeSessionRuntimeId);
    } else {
      await api.interruptSession(activeSessionId);
    }
    await Promise.allSettled([
      sessionsStoreApi.refresh(),
      activeSessionRuntimeId
        ? liveSessionStoreApi.loadInitial(activeSessionId, activeSessionRuntimeId)
        : liveSessionStoreApi.loadInitial(activeSessionId),
      activeSessionRuntimeId
        ? sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSession?.agent_backend, runtimeId: activeSessionRuntimeId })
        : sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSession?.agent_backend }),
    ]);
  }, [activeSession?.agent_backend, activeSessionBusy, activeSessionId, activeSessionRuntimeId, liveSessionStoreApi, sessionUiStoreApi, sessionsStoreApi]);

  const handleRuntimeSettingsSaved = useCallback(async (response: SwitchSessionModelResponse) => {
    if (activeSession) {
      sessionsStoreApi.upsertSession({
        ...activeSession,
        model: response.model ?? activeSession.model,
        provider_choice: response.provider ?? activeSession.provider_choice,
        reasoning_effort: response.reasoning_effort ?? activeSession.reasoning_effort,
      });
    }
    await sessionsStoreApi.refresh();
  }, [activeSession, sessionsStoreApi]);

  const triggerTestPushNotification = async () => {
    if (!notificationsSupported) {
      setVoiceSettingsStatus("Notifications are unavailable on this backend.");
      return;
    }
    setVoiceSettingsStatus("Sending test push...");
    try {
      const response = await api.triggerTestPushNotification() as { sent_count?: number; failed_count?: number; target_count?: number };
      const sent = Number(response.sent_count || 0);
      const failed = Number(response.failed_count || 0);
      const target = Number(response.target_count || sent + failed);
      if (sent > 0 && failed <= 0) {
        setVoiceSettingsStatus(`Test push sent to ${sent} device${sent === 1 ? "" : "s"}.`);
        return;
      }
      setVoiceSettingsStatus(`Test push sent to ${sent}/${target} devices${failed > 0 ? ` (${failed} failed)` : ""}.`);
    } catch (error) {
      setVoiceSettingsStatus(error instanceof Error ? `test push error: ${error.message}` : "test push error: unknown error");
    }
  };

  const handleBrandClick = () => {
    setSidebarOpen(false);
    if (announcementEnabled) {
      void startAnnouncementPlayback(voiceSettings, { resetSource: true, force: true });
    }
  };

  const codexSessionFileState = useSessionFileViewState({
    active: desktopGlobalView === "codex_sessions",
    activeCwd: activeCodexSessionCwd,
    onRenamed: () => {
      void sessionsStoreApi.refresh();
    },
  });

  const renderSessionsRail = () => (
    <AppShellSidebar
      activeView={desktopGlobalView}
      activeTeamId={selectedTeamId}
      codexSessionFileRail={<SessionFileRail state={codexSessionFileState} />}
      teamsData={teamsData}
      onNewSession={() => setNewSessionOpen(true)}
      onOpenSettings={() => openVoiceSettings()}
      onLogout={() => {
        void logout();
      }}
      onTeamSelect={setSelectedTeamId}
    />
  );


  return (
    <>
      <div className={shellClassName} data-testid="app-shell" style={shellStyle}>
        <audio ref={liveAudioRef} className="liveAudioElement" preload="none" />
        {mobileLayout ? (
          <MobileShell
            activeSessionId={activeSessionId}
            activeTitle={activeTitle}
            announcementEnabled={announcementEnabled}
            announcementLabel={announcementLabel}
            canInterrupt={Boolean(activeSessionId && activeSessionBusy)}
            statusItems={conversationStatusItems}
            notificationLabel={notificationLabel}
            notificationsEnabled={notificationsEnabled}
            onInterrupt={() => {
              void interruptActiveSession();
            }}
            onLogout={() => {
              void logout();
            }}
            onNewSession={() => setNewSessionOpen(true)}
            onOpenFilePath={(path, line) => openFileViewer(path, line ?? null, "file")}
            onOpenRuntimeSettings={() => setRuntimeSettingsOpen(true)}
            onOpenSettings={() => openVoiceSettings()}
            onToggleAnnouncements={() => {
              void toggleAnnouncements();
              if (!announcementEnabled) {
                void startAnnouncementPlayback(voiceSettings, { resetSource: true, force: true });
              }
            }}
            onToggleNotifications={() => {
              void toggleNotifications();
            }}
          />
        ) : (
          <>
            <GlobalNavRail
              activeView={desktopGlobalView}
              onBrandClick={handleBrandClick}
              onViewChange={setDesktopGlobalView}
            />
            <div className="desktopHiddenLegacyActions" aria-hidden="false">
              <button type="button" aria-label={notificationLabel} title={notificationLabel} onClick={() => { void toggleNotifications(); }} />
              <button type="button" aria-label={announcementLabel} title={announcementLabel} onClick={() => {
                void toggleAnnouncements();
                if (!announcementEnabled) {
                  void startAnnouncementPlayback(voiceSettings, { resetSource: true, force: true });
                }
              }} />
            </div>
            <aside className="sidebarColumn desktopSessionsRail">{renderSessionsRail()}</aside>
            <div
              aria-label="Resize sessions sidebar"
              aria-orientation="vertical"
              aria-valuemax={SIDEBAR_WIDTH_MAX_PX}
              aria-valuemin={SIDEBAR_WIDTH_MIN_PX}
              aria-valuenow={sidebarWidthPx}
              className="sidebarResizeHandle"
              onKeyDown={handleSidebarResizeKeyDown}
              onMouseDown={beginSidebarResize}
              onPointerDown={beginSidebarResize}
              role="separator"
              tabIndex={0}
              title="Drag to resize sessions"
            />
            {desktopGlobalView === "sessions" ? (
              <section className="conversationColumn">
                <AppShellToolbar
                  activeSessionId={activeSessionId}
                  activeTitle={activeTitle}
                  canInterrupt={Boolean(activeSessionId && activeSessionBusy)}
                  canProbeRuntime={Boolean(activeSessionId && activeBackendCapabilities?.runtime_probe === true && !activeSessionPending)}
                  inboxCount={activeQueueCount}
                  probingRuntime={runtimeProbePending}
                  showInterruptAction={showInterruptAction}
                  statusItems={conversationStatusItems}
                  showMobileSessionsTrigger={false}
                  showMobileToolbarMenu={false}
                  onInterrupt={() => {
                    void interruptActiveSession();
                  }}
                  onProbeRuntime={() => {
                    void probeActiveSessionState();
                  }}
                  onOpenFiles={() => openFileViewer()}
                  onOpenInbox={() => setInboxOpen(true)}
                  onOpenSessions={() => setSidebarOpen(true)}
                  onOpenWorkspace={() => openWorkspace("metadata")}
                />
                <ConversationPane
                  key={activeSessionId || "no-session"}
                  onOpenFilePath={(path, line) => openFileViewer(path, line ?? null, "file")}
                />
                <ConversationStateTray />
                <Composer />
              </section>
            ) : desktopGlobalView === "ask_user" ? (
              <AskUserView />
            ) : desktopGlobalView === "teams" ? (
              <TeamsThreadView selectedActorId={selectedTeamId} data={teamsData} />
            ) : desktopGlobalView === "codex_sessions" ? (
              <SessionFileDetail state={codexSessionFileState} />
            ) : (
              <SchedulerView />
            )}
          </>
        )}
      </div>
      <SessionRuntimeSettingsDialog
        defaults={newSessionDefaults}
        open={runtimeSettingsOpen}
        session={activeSession}
        onClose={() => setRuntimeSettingsOpen(false)}
        onRefreshDefaults={() => sessionsStoreApi.refreshBootstrap({ refreshPiModels: true })}
        onSaved={handleRuntimeSettingsSaved}
      />
      <AppShellWorkspaceOverlays
        activeSessionId={activeSessionId}
        activeSessionRuntimeId={activeSessionRuntimeId}
        fileViewerLine={fileViewerLine}
        fileViewerMode={fileViewerMode}
        fileViewerOpen={fileViewerOpen}
        fileViewerPath={fileViewerPath}
        fileViewerRequestKey={fileViewerRequestKey}
        inboxOpen={inboxOpen}
        newSessionOpen={newSessionOpen}
        sessionsRail={renderSessionsRail()}
        sidebarOpen={sidebarOpen}
        voiceSettingsDialog={(
          <VoiceSettingsDialog
            audioMeta={{
              enabledDevices: voiceSettings.notifications?.enabled_devices ?? 0,
              lastError: String(voiceSettings.audio?.last_error || ""),
              listeners: voiceSettings.audio?.active_listener_count ?? 0,
              queue: voiceSettings.audio?.queue_depth ?? 0,
              segments: voiceSettings.audio?.segment_count ?? 0,
              totalDevices: voiceSettings.notifications?.total_devices ?? 0,
            }}
            enterToSendDraft={enterToSendDraft}
            narrationEnabledDraft={narrationEnabledDraft}
            open={voiceSettingsOpen}
            replySoundEnabled={replySoundEnabled}
            status={voiceSettingsStatus}
            themeMode={themeMode}
            transportStatus={realtimeTransport}
            conversationFontSizePxDraft={displaySettingsDraft.conversationFontSizePx}
            composerFontSizePxDraft={displaySettingsDraft.composerFontSizePx}
            bufferAssistantOutputDraft={displaySettingsDraft.bufferAssistantOutput}
            supervisorProviderApiKeyDraft={supervisorProviderApiKeyDraft}
            supervisorProviderBaseUrlDraft={supervisorProviderBaseUrlDraft}
            supervisorProviderModelDraft={supervisorProviderModelDraft}
            supervisorProviderStatus={supervisorProviderStatus}
            voiceApiKeyDraft={voiceApiKeyDraft}
            voiceBaseUrlDraft={voiceBaseUrlDraft}
            onChangeEnterToSend={setEnterToSendDraft}
            onChangeNarrationEnabled={setNarrationEnabledDraft}
            onChangeReplySoundEnabled={setReplySoundEnabled}
            onChangeThemeMode={setThemeMode}
            onChangeConnectWireFormat={(value) => {
              setConnectWireFormat(value);
              void sessionsStoreApi.refreshBootstrap();
            }}
            onChangeConversationFontSizePx={(value) => setDisplaySettingsDraft((current) => ({ ...current, conversationFontSizePx: value }))}
            onChangeComposerFontSizePx={(value) => setDisplaySettingsDraft((current) => ({ ...current, composerFontSizePx: value }))}
            onChangeBufferAssistantOutput={(value) => setDisplaySettingsDraft((current) => ({ ...current, bufferAssistantOutput: value }))}
            onChangeSupervisorProviderApiKey={setSupervisorProviderApiKeyDraft}
            onChangeSupervisorProviderBaseUrl={setSupervisorProviderBaseUrlDraft}
            onChangeSupervisorProviderModel={setSupervisorProviderModelDraft}
            onChangeVoiceApiKey={setVoiceApiKeyDraft}
            onChangeVoiceBaseUrl={setVoiceBaseUrlDraft}
            onClose={closeVoiceSettings}
            onSave={() => {
              void saveSettings();
            }}
            onTestProvider={() => {
              void testVoiceProvider();
            }}
            onTriggerTestPush={() => {
              void triggerTestPushNotification();
            }}
          />
        )}
        workspaceDetails={renderWorkspaceDetails()}
        workspaceOpen={workspaceOpen}
        onCloseFileViewer={closeFileViewer}
        onCloseInbox={() => setInboxOpen(false)}
        onCloseNewSession={() => setNewSessionOpen(false)}
        onCloseSidebar={() => setSidebarOpen(false)}
        onCloseWorkspace={() => {
          setWorkspaceOpen(false);
        }}
      />
    </>
  );
}
