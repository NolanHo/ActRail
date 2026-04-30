import { lazy, Suspense } from "preact/compat";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { api } from "../lib/api";
import { ConversationPane } from "../components/conversation/ConversationPane";
import { ConversationStateTray } from "../components/conversation/ConversationStateTray";
import { Composer } from "../components/composer/Composer";
import type { FileViewMode } from "../components/workspace/FileViewerDialog";
import { AppShellSidebar } from "./app-shell/AppShellSidebar";
import { AppShellToolbar, type ConversationStatusItem } from "./app-shell/AppShellToolbar";
import { AppShellWorkspaceOverlays } from "./app-shell/AppShellWorkspaceOverlays";
import { MobileShell } from "./app-shell/MobileShell";
import { VoiceSettingsDialog } from "./app-shell/VoiceSettingsDialog";
import { useAppShellAudio } from "./app-shell/useAppShellAudio";
import { useAppShellEvents } from "./app-shell/useAppShellEvents";
import { useAppShellNotifications } from "./app-shell/useAppShellNotifications";
import { useAppShellSessionEffects } from "./app-shell/useAppShellSessionEffects";
import { setConnectTransportOptIn, setConnectWireFormat } from "../domains/sessions/store";
import { useLiveSessionStore, useLiveSessionStoreApi, useMessagesStore, useSessionUiStore, useSessionUiStoreApi, useSessionsStore, useSessionsStoreApi, useWaitsStore, useWaitsStoreApi } from "./providers";
import {
  applyThemeMode,
  readThemeMode,
  shouldUseMobileWorkspaceSheet,
  shortSessionId,
  writeThemeMode,
} from "./app-shell/utils";
import { getSessionRuntimeId } from "../lib/session-identity";
import { getSessionDisplayName } from "../lib/session-display";
import { applyUserDisplaySettings, readUserDisplaySettings, writeUserDisplaySettings } from "../lib/user-settings";

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
  const { bySessionId } = useMessagesStore();
  const { activeSessionId, bootstrapCapabilities, bootstrapLoaded, items, realtimeTransport } = useSessionsStore();
  const { busyBySessionId, contextUsageBySessionId, generatingBySessionId } = useLiveSessionStore() as {
    busyBySessionId: Record<string, boolean>;
    generatingBySessionId?: Record<string, boolean>;
    contextUsageBySessionId?: Record<string, { used_tokens?: number; total_tokens?: number; percent_used?: number } | null>;
  };
  const { sessionId: sessionUiSessionId, diagnostics } = useSessionUiStore();
  const waitsState = useWaitsStore();
  const sessionsStoreApi = useSessionsStoreApi();
  const liveSessionStoreApi = useLiveSessionStoreApi();
  const sessionUiStoreApi = useSessionUiStoreApi();
  const waitsStoreApi = useWaitsStoreApi();
  const [newSessionOpen, setNewSessionOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [workspaceOpen, setWorkspaceOpen] = useState(false);
  const [fileViewerOpen, setFileViewerOpen] = useState(false);
  const [harnessOpen, setHarnessOpen] = useState(false);
  const [workspaceInitialTab, setWorkspaceInitialTab] = useState<"insight" | "overview">("insight");
  const [fileViewerPath, setFileViewerPath] = useState("");
  const [fileViewerLine, setFileViewerLine] = useState<number | null>(null);
  const [fileViewerMode, setFileViewerMode] = useState<FileViewMode | null>(null);
  const [fileViewerRequestKey, setFileViewerRequestKey] = useState(0);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [themeMode, setThemeMode] = useState(() => readThemeMode());
  const [displaySettings, setDisplaySettings] = useState(() => readUserDisplaySettings());
  const [displaySettingsDraft, setDisplaySettingsDraft] = useState(() => readUserDisplaySettings());
  const [realtimeConnected, setRealtimeConnected] = useState(false);
  const voiceSupported = bootstrapCapabilities?.voice !== false;
  const harnessSupported = bootstrapCapabilities?.harness !== false;
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
    if (voiceSettingsOpen) {
      setDisplaySettingsDraft(displaySettings);
    }
  }, [displaySettings, voiceSettingsOpen]);

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
  const activeSessionGenerating = Boolean(activeSessionId && generatingBySessionId?.[activeSessionId] === true);
  const activeSessionHasLiveBusy = Boolean(activeSessionId && Object.prototype.hasOwnProperty.call(busyBySessionId, activeSessionId));
  const activeSessionLiveBusy = Boolean(activeSessionId && busyBySessionId[activeSessionId] === true);
  const activeWait = activeSessionId ? waitsState.activeBySessionId[activeSessionId] ?? activeSession?.active_wait ?? null : null;
  const activeSessionBusy = Boolean(
    activeSessionGenerating
    || (activeSessionHasLiveBusy ? activeSessionLiveBusy : activeSession?.busy === true),
  );
  const activeTitle = activeSession
    ? getSessionDisplayName(activeSession, shortSessionId(activeSession.session_id))
    : "No session selected";
  const activeSessionDiagnostics = sessionUiSessionId === activeSessionId && diagnostics && typeof diagnostics === "object"
    ? diagnostics as { model?: unknown; reasoning_effort?: unknown; context_usage?: unknown }
    : null;
  const diagnosticsModel = typeof activeSessionDiagnostics?.model === "string" ? activeSessionDiagnostics.model.trim() : "";
  const diagnosticsReasoningEffort = typeof activeSessionDiagnostics?.reasoning_effort === "string" ? activeSessionDiagnostics.reasoning_effort.trim() : "";
  const diagnosticsContextUsage = activeSessionDiagnostics?.context_usage && typeof activeSessionDiagnostics.context_usage === "object"
    ? activeSessionDiagnostics.context_usage as { used_tokens?: number; total_tokens?: number; percent_used?: number }
    : null;
  const activeModel = diagnosticsModel || (typeof activeSession?.model === "string" ? activeSession.model.trim() : "");
  const activeReasoningEffort = diagnosticsReasoningEffort || (typeof activeSession?.reasoning_effort === "string" ? activeSession.reasoning_effort.trim() : "");
  const activeContextUsageLabel = activeSessionId ? contextUsageStatusLabel(contextUsageBySessionId?.[activeSessionId] ?? diagnosticsContextUsage) : "";
  const activeQueueCount = typeof activeSession?.queue_len === "number" && Number.isFinite(activeSession.queue_len)
    ? Math.max(0, Math.round(activeSession.queue_len))
    : 0;
  const conversationStatusItems = useMemo<ConversationStatusItem[]>(() => {
    if (!activeSession) {
      return [];
    }
    const items: ConversationStatusItem[] = [];
    if (activeSession.agent_backend) {
      items.push({ label: "Backend", value: activeSession.agent_backend });
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
      const version = [activeSession.iod.git_sha, activeSession.iod.build_date].filter(Boolean).join(" ");
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
    if (activeWait) {
      items.push({ label: "Wait", value: "user input", tone: "attention" });
    }
    if (activeSession.reset_required === true || activeSession.transport_state === "broken") {
      items.push({ label: "Runtime", value: "broken", tone: "error" });
    } else if (activeSession.transport_state === "stalled") {
      items.push({ label: "Runtime", value: "stalled", tone: "error" });
    } else if (activeSession.pending_startup === true) {
      items.push({ label: "Runtime", value: "starting", tone: "attention" });
    } else if (activeSessionGenerating) {
      items.push({ label: "Runtime", value: "generating", tone: "attention" });
    } else if (activeSessionBusy) {
      items.push({ label: "Runtime", value: "running", tone: "attention" });
    }
    return items;
  }, [activeContextUsageLabel, activeModel, activeQueueCount, activeReasoningEffort, activeSession, activeSessionBusy, activeSessionGenerating, activeWait]);

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
    toggleNotifications,
  } = useAppShellNotifications({
    activeSessionId,
    activeTitle,
    bootstrapLoaded,
    bySessionId,
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
    sessionUiStoreApi,
    sessionsStoreApi,
    waitsStoreApi,
    workspaceOpen: workspaceOpen || detailsOpen,
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
    workspaceOpen: workspaceOpen || detailsOpen,
    activeSessionReplySoundPrimingRef,
    suppressedReplySoundSessionIdsRef,
  });

  const sessionUiMatchesActiveSession = !!activeSessionId && sessionUiSessionId === activeSessionId;
  const showInterruptAction = !activeSessionId || activeSessionBusy;
  const [mobileLayout, setMobileLayout] = useState(() => shouldUseMobileWorkspaceSheet());

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
    setHarnessOpen(false);
    setWorkspaceInitialTab("insight");
  }, [activeSessionId]);

  const shellClassName = useMemo(() => ["appShell", "editorialShell"].join(" "), []);

  const renderWorkspaceDetails = () => (
    sessionUiMatchesActiveSession || activeWait ? (
      <Suspense fallback={<WorkspaceLoadingFallback />}>
        <LazySessionWorkspace mode="details" initialTab={workspaceInitialTab} />
      </Suspense>
    ) : <EmptyDetailsWorkspace />
  );

  const openWorkspace = (initialTab: "insight" | "overview" = "overview") => {
    setWorkspaceInitialTab(initialTab);
    setWorkspaceOpen(true);
  };

  const openWaitDetails = () => {
    if (activeWait && activeSessionId) {
      waitsStoreApi.openWait({ ...activeWait, session_id: activeWait.session_id || activeSessionId });
    }
    setDetailsOpen(true);
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

  const renderSessionsRail = () => (
    <AppShellSidebar
      announcementEnabled={announcementEnabled}
      announcementLabel={announcementLabel}
      notificationLabel={notificationLabel}
      notificationsEnabled={notificationsEnabled}
      onBrandClick={() => {
        setSidebarOpen(false);
        if (announcementEnabled) {
          void startAnnouncementPlayback(voiceSettings, { resetSource: true, force: true });
        }
      }}
      onNewSession={() => setNewSessionOpen(true)}
      onOpenSettings={() => openVoiceSettings()}
      onLogout={() => {
        void logout();
      }}
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
            onOpenFiles={() => openFileViewer()}
            onOpenHarness={() => setHarnessOpen(true)}
            onOpenSettings={() => openVoiceSettings()}
            onOpenInsight={() => openWorkspace("insight")}
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
            <aside className="sidebarColumn desktopSessionsRail">{renderSessionsRail()}</aside>
            <section className="conversationColumn">
              <AppShellToolbar
                activeSessionId={activeSessionId}
                activeTitle={activeTitle}
                canInterrupt={Boolean(activeSessionId && activeSessionBusy)}
                showInterruptAction={showInterruptAction}
                statusItems={conversationStatusItems}
                showMobileSessionsTrigger={false}
                showMobileToolbarMenu={false}
                onInterrupt={() => {
                  void interruptActiveSession();
                }}
                onOpenFiles={() => openFileViewer()}
                onOpenHarness={() => setHarnessOpen(true)}
                onOpenSessions={() => setSidebarOpen(true)}
                onOpenInsight={() => openWorkspace("insight")}
                onOpenWaits={openWaitDetails}
                onOpenWorkspace={() => openWorkspace("overview")}
              />
              <ConversationPane
                key={activeSessionId || "no-session"}
                onOpenFilePath={(path, line) => openFileViewer(path, line ?? null, "file")}
              />
              <ConversationStateTray />
              <Composer />
            </section>
          </>
        )}
      </div>
      <AppShellWorkspaceOverlays
        activeSessionId={activeSessionId}
        activeSessionRuntimeId={activeSessionRuntimeId}
        detailsOpen={detailsOpen}
        fileViewerLine={fileViewerLine}
        fileViewerMode={fileViewerMode}
        fileViewerOpen={fileViewerOpen}
        fileViewerPath={fileViewerPath}
        fileViewerRequestKey={fileViewerRequestKey}
        harnessOpen={harnessOpen}
        harnessSupported={harnessSupported}
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
            onChangeVoiceApiKey={setVoiceApiKeyDraft}
            onChangeVoiceBaseUrl={setVoiceBaseUrlDraft}
            onClose={closeVoiceSettings}
            onSave={() => {
              setDisplaySettings(displaySettingsDraft);
              writeUserDisplaySettings(displaySettingsDraft);
              void saveVoiceSettings();
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
        onCloseDetails={() => setDetailsOpen(false)}
        onCloseFileViewer={closeFileViewer}
        onCloseHarness={() => setHarnessOpen(false)}
        onCloseNewSession={() => setNewSessionOpen(false)}
        onCloseSidebar={() => setSidebarOpen(false)}
        onCloseWorkspace={() => setWorkspaceOpen(false)}
      />
    </>
  );
}
