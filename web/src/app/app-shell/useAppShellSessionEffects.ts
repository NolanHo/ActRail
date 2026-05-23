import { useEffect, useRef, useState } from "preact/hooks";
import type { LiveSessionStore } from "../../domains/live-session/store";
import type { SessionUiStore } from "../../domains/session-ui/store";
import type { SessionsStore } from "../../domains/sessions/store";
import { getSessionRuntimeId } from "../../lib/session-identity";
import type { SessionSummary } from "../../lib/types";

interface UseAppShellSessionEffectsOptions {
  activeSessionBackend?: string;
  activeSessionHistorical?: boolean;
  activeSessionPending?: boolean;
  activeSessionId: string | null;
  activeSessionRuntimeId?: string | null;
  activeSessionLiveBusy: boolean;
  items: SessionSummary[];
  liveSessionStoreApi: LiveSessionStore;
  realtimeConnected?: boolean;
  replySoundEnabled: boolean;
  sessionUiStoreApi: SessionUiStore;
  sessionsStoreApi: SessionsStore;
  workspaceOpen: boolean;
  activeSessionReplySoundPrimingRef: { current: string | null };
  backgroundReplySoundPrimedSessionIdsRef: { current: Set<string> };
  suppressedReplySoundSessionIdsRef: { current: Set<string> };
}

const BUSY_SESSIONS_REFRESH_MS = 5000;
const IDLE_SESSIONS_REFRESH_MS = 15000;
const ACTIVE_BUSY_LIVE_REFRESH_MS = 3000;
const ACTIVE_IDLE_LIVE_REFRESH_MS = 30000;
const ACTIVE_REALTIME_BUSY_LIVE_REFRESH_MS = 5000;
const ACTIVE_REALTIME_IDLE_LIVE_REFRESH_MS = 15000;
const BACKGROUND_BUSY_LIVE_REFRESH_MS = 5000;
const WORKSPACE_REFRESH_MS = 15000;
const REALTIME_SESSIONS_RECOVERY_MS = 30000;
const REALTIME_WORKSPACE_RECOVERY_MS = 60000;
const MARK_SESSION_READ_DELAY_MS = 3000;

function isDocumentVisible() {
  if (typeof document === "undefined") {
    return true;
  }
  return document.visibilityState !== "hidden";
}

export function useAppShellSessionEffects({
  activeSessionBackend,
  activeSessionHistorical,
  activeSessionPending,
  activeSessionId,
  activeSessionRuntimeId,
  activeSessionLiveBusy,
  items,
  liveSessionStoreApi,
  realtimeConnected = false,
  replySoundEnabled,
  sessionUiStoreApi,
  sessionsStoreApi,
  workspaceOpen,
  activeSessionReplySoundPrimingRef,
  backgroundReplySoundPrimedSessionIdsRef,
  suppressedReplySoundSessionIdsRef,
}: UseAppShellSessionEffectsOptions) {
  const [pageVisible, setPageVisible] = useState(isDocumentVisible);
  const backgroundLivePrimedSessionIdsRef = useRef(new Set<string>());
  const hasBusySession = items.some((session) => Boolean(session.busy || session.pending_startup));
  const sessionsRefreshIntervalMs = realtimeConnected
    ? REALTIME_SESSIONS_RECOVERY_MS
    : (hasBusySession ? BUSY_SESSIONS_REFRESH_MS : IDLE_SESSIONS_REFRESH_MS);
  const activeSessionBusy = activeSessionLiveBusy
    || items.some((session) => session.session_id === activeSessionId && session.busy);
  const activeLiveRefreshIntervalMs = realtimeConnected
    ? activeSessionBusy
      ? ACTIVE_REALTIME_BUSY_LIVE_REFRESH_MS
      : ACTIVE_REALTIME_IDLE_LIVE_REFRESH_MS
    : activeSessionBusy
      ? ACTIVE_BUSY_LIVE_REFRESH_MS
      : ACTIVE_IDLE_LIVE_REFRESH_MS;

  useEffect(() => {
    const handleVisibilityChange = () => {
      setPageVisible(isDocumentVisible());
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);
    return () => document.removeEventListener("visibilitychange", handleVisibilityChange);
  }, []);

  useEffect(() => {
    if (!pageVisible) {
      return undefined;
    }

    sessionsStoreApi.refresh().catch(() => undefined);
    const intervalId = window.setInterval(() => {
      sessionsStoreApi.refresh().catch(() => undefined);
    }, sessionsRefreshIntervalMs);
    return () => window.clearInterval(intervalId);
  }, [pageVisible, sessionsRefreshIntervalMs, sessionsStoreApi]);

  useEffect(() => {
    if (!pageVisible || !activeSessionId) {
      return undefined;
    }

    if ((activeSessionHistorical && activeSessionBackend === "pi") || activeSessionPending) {
      return undefined;
    }

    const recoverMissingSession = (error: unknown) => {
      if (!error || typeof error !== "object" || (error as { status?: unknown }).status !== 404) {
        return;
      }
      sessionsStoreApi.select(null);
      sessionsStoreApi.refresh().catch(() => undefined);
    };

    if (replySoundEnabled) {
      suppressedReplySoundSessionIdsRef.current.add(activeSessionId);
    }
    (activeSessionRuntimeId
      ? liveSessionStoreApi.loadInitial(activeSessionId, activeSessionRuntimeId)
      : liveSessionStoreApi.loadInitial(activeSessionId))
      .catch(recoverMissingSession);
    if (activeSessionReplySoundPrimingRef.current === activeSessionId) {
      suppressedReplySoundSessionIdsRef.current.delete(activeSessionId);
      activeSessionReplySoundPrimingRef.current = null;
    }
    if (activeLiveRefreshIntervalMs === null) {
      return undefined;
    }

    const intervalId = window.setInterval(() => {
      (activeSessionRuntimeId
        ? liveSessionStoreApi.poll(activeSessionId, activeSessionRuntimeId)
        : liveSessionStoreApi.poll(activeSessionId))
        .catch(recoverMissingSession);
    }, activeLiveRefreshIntervalMs);
    return () => window.clearInterval(intervalId);
  }, [activeLiveRefreshIntervalMs, activeSessionBackend, activeSessionHistorical, activeSessionId, activeSessionPending, activeSessionReplySoundPrimingRef, activeSessionRuntimeId, liveSessionStoreApi, pageVisible, replySoundEnabled, sessionsStoreApi, suppressedReplySoundSessionIdsRef]);

  useEffect(() => {
    if (!pageVisible || !activeSessionId) {
      return undefined;
    }
    const activeSession = items.find((session) => session.session_id === activeSessionId);
    if (activeSession?.has_unread_assistant !== true) {
      return undefined;
    }
    const timerId = window.setTimeout(() => {
      if (sessionsStoreApi.getState().activeSessionId !== activeSessionId) {
        return;
      }
      sessionsStoreApi.markRead(activeSessionId).catch(() => undefined);
    }, MARK_SESSION_READ_DELAY_MS);
    return () => window.clearTimeout(timerId);
  }, [activeSessionId, items, pageVisible, sessionsStoreApi]);

  useEffect(() => {
    if (!pageVisible || !workspaceOpen || !activeSessionId) {
      return undefined;
    }

    if ((activeSessionHistorical && activeSessionBackend === "pi") || activeSessionPending) {
      return undefined;
    }

    const recoverMissingSession = (error: unknown) => {
      if (!error || typeof error !== "object" || (error as { status?: unknown }).status !== 404) {
        return;
      }
      sessionsStoreApi.select(null);
      sessionsStoreApi.refresh().catch(() => undefined);
    };

    (activeSessionRuntimeId
      ? sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSessionBackend, runtimeId: activeSessionRuntimeId })
      : sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSessionBackend })).catch(recoverMissingSession);
    const intervalId = window.setInterval(() => {
      (activeSessionRuntimeId
        ? sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSessionBackend, runtimeId: activeSessionRuntimeId })
        : sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSessionBackend })).catch(recoverMissingSession);
    }, realtimeConnected ? REALTIME_WORKSPACE_RECOVERY_MS : WORKSPACE_REFRESH_MS);
    return () => window.clearInterval(intervalId);
  }, [activeSessionBackend, activeSessionHistorical, activeSessionId, activeSessionPending, activeSessionRuntimeId, pageVisible, realtimeConnected, sessionUiStoreApi, sessionsStoreApi, workspaceOpen]);

  useEffect(() => {
    if (!pageVisible) {
      return;
    }

    const backgroundBusySessions = items.filter((session) => session.session_id !== activeSessionId && session.busy);
    for (const session of backgroundBusySessions) {
      const sessionId = session.session_id;
      const runtimeId = getSessionRuntimeId(session);
      if (backgroundLivePrimedSessionIdsRef.current.has(sessionId)) {
        continue;
      }
      backgroundLivePrimedSessionIdsRef.current.add(sessionId);
      const shouldPrimeReplySound = replySoundEnabled
        && !backgroundReplySoundPrimedSessionIdsRef.current.has(sessionId)
        && !suppressedReplySoundSessionIdsRef.current.has(sessionId);
      if (shouldPrimeReplySound) {
        suppressedReplySoundSessionIdsRef.current.add(sessionId);
      }
      (runtimeId
        ? liveSessionStoreApi.loadInitial(sessionId, runtimeId)
        : liveSessionStoreApi.loadInitial(sessionId))
        .catch(() => undefined)
        .finally(() => {
          if (shouldPrimeReplySound) {
            suppressedReplySoundSessionIdsRef.current.delete(sessionId);
            backgroundReplySoundPrimedSessionIdsRef.current.add(sessionId);
          }
        });
    }
  }, [activeSessionId, backgroundReplySoundPrimedSessionIdsRef, items, liveSessionStoreApi, pageVisible, replySoundEnabled, suppressedReplySoundSessionIdsRef]);

  useEffect(() => {
    if (!pageVisible) {
      return undefined;
    }

    const pollBackgroundBusySessions = () => {
      const backgroundBusySessions = items
        .filter((session) => session.session_id !== activeSessionId)
        .filter((session) => session.busy || backgroundLivePrimedSessionIdsRef.current.has(session.session_id));
      for (const session of backgroundBusySessions) {
        const runtimeId = getSessionRuntimeId(session);
        if (!session.busy) {
          backgroundLivePrimedSessionIdsRef.current.delete(session.session_id);
        }
        (runtimeId
          ? liveSessionStoreApi.poll(session.session_id, runtimeId)
          : liveSessionStoreApi.poll(session.session_id))
          .catch(() => undefined);
      }
    };

    pollBackgroundBusySessions();
    const intervalId = window.setInterval(
      pollBackgroundBusySessions,
      BACKGROUND_BUSY_LIVE_REFRESH_MS,
    );
    return () => window.clearInterval(intervalId);
  }, [activeSessionId, items, liveSessionStoreApi, pageVisible]);
}
