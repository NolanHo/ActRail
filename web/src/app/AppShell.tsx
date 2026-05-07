import { lazy, Suspense } from "preact/compat";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { api } from "../lib/api";
import { ConversationPane } from "../components/conversation/ConversationPane";
import { ConversationStateTray } from "../components/conversation/ConversationStateTray";
import { Composer } from "../components/composer/Composer";
import type { FileViewMode } from "../components/workspace/FileViewerDialog";
import { AppShellSidebar, GlobalNavRail, type DesktopGlobalView } from "./app-shell/AppShellSidebar";
import { AppShellToolbar, type ConversationStatusItem } from "./app-shell/AppShellToolbar";
import { AppShellWorkspaceOverlays } from "./app-shell/AppShellWorkspaceOverlays";
import { SchedulerView } from "../components/scheduler/SchedulerView";
import { TeamsThreadView, useTeamsData } from "../components/teams/TeamsView";
import { AskUserView } from "../components/waits/AskUserView";
import { MobileShell } from "./app-shell/MobileShell";
import { VoiceSettingsDialog } from "./app-shell/VoiceSettingsDialog";
import { useAppShellAudio } from "./app-shell/useAppShellAudio";
import { useAppShellEvents } from "./app-shell/useAppShellEvents";
import { useAppShellNotifications } from "./app-shell/useAppShellNotifications";
import { useAppShellSessionEffects } from "./app-shell/useAppShellSessionEffects";
import { setConnectTransportOptIn, setConnectWireFormat } from "../domains/sessions/store";
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
  readThemeMode,
  shouldUseMobileLayout,
  shortSessionId,
  writeThemeMode,
} from "./app-shell/utils";
import { getSessionRuntimeId } from "../lib/session-identity";
import { getSessionDisplayName } from "../lib/session-display";
import { applyUserDisplaySettings, readUserDisplaySettings, writeUserDisplaySettings } from "../lib/user-settings";

type WorkspaceTab = "metadata";
type FinalResponseSignature = {
  key: string;
  notificationText: string;
  sessionId: string;
};

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
        <h3>Queue</h3>
        <ul className="workspaceList">
          <li>No queued items</li>
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
  const { activeSessionId, bootstrapCapabilities, bootstrapLoaded, items, realtimeTransport } = useSessionsStoreSelector((state) => ({
    activeSessionId: state.activeSessionId,
    bootstrapCapabilities: state.bootstrapCapabilities,
    bootstrapLoaded: state.bootstrapLoaded,
    items: state.items,
    realtimeTransport: state.realtimeTransport,
  }), shallowEqual);
  const liveActiveSessionState = useLiveSessionStoreSelector((state) => {
    if (!activeSessionId) {
      return {
        busy: undefined,
        contextUsage: null,
        generating: undefined,
        hasBusy: false,
      };
    }
    const busyBySessionId = state.busyBySessionId ?? {};
    const contextUsageBySessionId = state.contextUsageBySessionId ?? {};
    const generatingBySessionId = state.generatingBySessionId ?? {};
    return {
      busy: busyBySessionId[activeSessionId],
      contextUsage: contextUsageBySessionId[activeSessionId] ?? null,
      generating: generatingBySessionId[activeSessionId],
      hasBusy: Object.prototype.hasOwnProperty.call(busyBySessionId, activeSessionId),
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
  const [workspaceInitialTab, setWorkspaceInitialTab] = useState<WorkspaceTab>("metadata");
  const [fileViewerPath, setFileViewerPath] = useState("");
  const [fileViewerLine, setFileViewerLine] = useState<number | null>(null);
  const [fileViewerMode, setFileViewerMode] = useState<FileViewMode | null>(null);
  const [fileViewerRequestKey, setFileViewerRequestKey] = useState(0);
  const [sidebarOpen, setSidebarOpen] = useState(false);
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
  const activeSessionRuntimeId = getSessionRuntimeId(activeSession);
  const activeSessionPending = activeSession?.pending_startup === true;
  const activeSessionGenerating = Boolean(activeSessionId && liveActiveSessionState.generating === true);
  const activeSessionHasLiveBusy = Boolean(activeSessionId && liveActiveSessionState.hasBusy);
  const activeSessionLiveBusy = Boolean(activeSessionId && liveActiveSessionState.busy === true);
  const visibleActiveWait = activeWait ?? activeSession?.active_wait ?? null;
  const activeSessionBusy = Boolean(
    activeSessionGenerating
    || (activeSessionHasLiveBusy ? activeSessionLiveBusy : activeSession?.busy === true),
  );
  const activeTitle = activeSession
    ? getSessionDisplayName(activeSession, shortSessionId(activeSession.session_id))
    : "No session selected";
  const activeModel = typeof activeSession?.model === "string" ? activeSession.model.trim() : "";
  const activeReasoningEffort = typeof activeSession?.reasoning_effort === "string" ? activeSession.reasoning_effort.trim() : "";
  const activeContextUsageLabel = activeSessionId ? contextUsageStatusLabel(liveActiveSessionState.contextUsage) : "";
  const activeQueueCount = typeof activeSession?.queue_len === "number" && Number.isFinite(activeSession.queue_len)
    ? Math.max(0, Math.round(activeSession.queue_len))
    : 0;
  const conversationStatusItems = useMemo<ConversationStatusItem[]>(() => {
    if (!activeSession) {
      return [];
    }
    const items: ConversationStatusItem[] = [];
    if (activeSession.agent_backend) {
      const mode = typeof activeSession.iod?.mode === "string" ? activeSession.iod.mode.trim() : "";
      const backend = mode ? `${activeSession.agent_backend}/${mode}` : activeSession.agent_backend;
      items.push({ label: "Backend", value: backend });
    }
    if (activeModel) {
      items.push({ label: "Model", value: activeModel });
    }
    if (activeReasoningEffort) {
      items.push({ label: "Effort", value: activeReasoningEffort });
    }
    if (activeContextUsageLabel) {
      items.push({ label: "Context", value: activeContextUsageLabel });
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
      items.push({ label: "Queue", value: String(activeQueueCount), tone: "attention" });
    }
    if (visibleActiveWait) {
      items.push({ label: "Wait", value: "user input", tone: "attention" });
    }
    if (activeSession.reset_required === true || activeSession.transport_state === "broken") {
      items.push({ label: "Runtime", value: "broken", tone: "error" });
    } else if (activeSession.transport_state === "failed") {
      items.push({ label: "Runtime", value: "failed", tone: "error" });
    } else if (activeSession.transport_state === "ended") {
      items.push({ label: "Runtime", value: "ended", tone: "error" });
    } else if (activeSession.transport_state === "silent" || activeSession.transport_state === "stalled") {
      items.push({ label: "Runtime", value: activeSession.transport_state, tone: "error" });
    } else if (activeSession.pending_startup === true) {
      items.push({ label: "Runtime", value: "starting", tone: "attention" });
    } else if (activeSessionGenerating) {
      items.push({ label: "Runtime", value: "generating", tone: "busy" });
    } else if (activeSessionBusy) {
      items.push({ label: "Runtime", value: "busy", tone: "busy" });
    } else {
      items.push({ label: "Runtime", value: "idle", tone: "success" });
    }
    return items;
  }, [activeContextUsageLabel, activeModel, activeQueueCount, activeReasoningEffort, activeSession, activeSessionBusy, activeSessionGenerating, visibleActiveWait]);

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

    const mediaQuery = window.matchMedia("(max-width: 880px)");
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
    setWorkspaceInitialTab("metadata");
  }, [activeSessionId]);

  const shellClassName = useMemo(() => ["appShell", "editorialShell", "withGlobalNav"].join(" "), []);

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

  const interruptActiveSession = async () => {
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
  };

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.key !== "Escape") {
        return;
      }
      if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) {
        return;
      }
      if (!activeSessionId || !activeSessionBusy) {
        return;
      }
      event.preventDefault();
      void interruptActiveSession();
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [activeSessionBusy, activeSessionId, interruptActiveSession]);

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

  const renderSessionsRail = () => (
    <AppShellSidebar
      activeView={desktopGlobalView}
      activeTeamId={selectedTeamId}
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
      <div className={shellClassName} data-testid="app-shell">
        <audio ref={liveAudioRef} className="liveAudioElement" preload="none" />
        {mobileLayout ? (
          <MobileShell
            activeSessionId={activeSessionId}
            activeTitle={activeTitle}
            announcementEnabled={announcementEnabled}
            announcementLabel={announcementLabel}
            canInterrupt={Boolean(activeSessionId && activeSessionBusy)}
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
            {desktopGlobalView === "sessions" ? (
              <section className="conversationColumn">
                <AppShellToolbar
                  activeSessionId={activeSessionId}
                  activeTitle={activeTitle}
                  canInterrupt={Boolean(activeSessionId && activeSessionBusy)}
                  canProbeRuntime={Boolean(activeSessionId && activeSession?.agent_backend === "pi" && !activeSessionPending)}
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
            ) : (
              <SchedulerView />
            )}
          </>
        )}
      </div>
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
            onChangeTransportOptIn={(enabled) => {
              setConnectTransportOptIn(enabled);
              void sessionsStoreApi.refreshBootstrap();
            }}
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
