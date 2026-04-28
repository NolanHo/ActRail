import { useEffect, useRef, useState } from "preact/hooks";
import {
  connect,
  disconnect,
  setRealtimeSubscriptions,
  subscribeRealtimeFrames,
  subscribeRealtimeState,
} from "../../domains/realtime/client";
import type { RealtimeStreamSubscription } from "../../domains/realtime/client";
import type { LiveSessionStore } from "../../domains/live-session/store";
import type { SessionUiStore } from "../../domains/session-ui/store";
import type { SessionsStore } from "../../domains/sessions/store";
import type { RealtimeEnvelope, SessionSummary } from "../../lib/types";

interface UseAppShellEventsOptions {
  activeSessionBackend?: string;
  activeSessionHistorical?: boolean;
  activeSessionId: string | null;
  activeSessionPending?: boolean;
  activeSessionRuntimeId?: string | null;
  bootstrapLoaded: boolean;
  items: SessionSummary[];
  liveSessionStoreApi: LiveSessionStore;
  onConnectionChange?: (connected: boolean) => void;
  refreshNotificationsFeed: () => Promise<void>;
  sessionUiStoreApi: SessionUiStore;
  sessionsStoreApi: SessionsStore;
  workspaceOpen: boolean;
}

interface LatestAppShellEventContext {
  activeSessionBackend?: string;
  activeSessionHistorical?: boolean;
  activeSessionId: string | null;
  activeSessionPending?: boolean;
  activeSessionRuntimeId?: string | null;
  bootstrapLoaded: boolean;
  items: SessionSummary[];
  onConnectionChange?: (connected: boolean) => void;
  refreshNotificationsFeed: () => Promise<void>;
  workspaceOpen: boolean;
}

function isRecoverableNotFound(error: unknown) {
  return Boolean(error && typeof error === "object" && (error as { status?: unknown }).status === 404);
}

function sessionStreamName(sessionId: string) {
  return `session:${sessionId}`;
}

function sessionUiStreamName(sessionId: string) {
  return `session:${sessionId}:ui`;
}

function resolveSessionId(frame: RealtimeEnvelope) {
  const payload = frame.payload && typeof frame.payload === "object" ? frame.payload as Record<string, unknown> : null;
  if (typeof payload?.session_id === "string" && payload.session_id.trim()) {
    return payload.session_id.trim();
  }
  const stream = String(frame.stream || "").trim();
  if (!stream.startsWith("session:")) {
    return "";
  }
  const [, rest] = stream.split("session:");
  return String(rest || "").split(":")[0] || "";
}

export function useAppShellEvents({
  activeSessionBackend,
  activeSessionHistorical,
  activeSessionId,
  activeSessionPending,
  activeSessionRuntimeId,
  bootstrapLoaded,
  items,
  liveSessionStoreApi,
  onConnectionChange,
  refreshNotificationsFeed,
  sessionUiStoreApi,
  sessionsStoreApi,
  workspaceOpen,
}: UseAppShellEventsOptions) {
  const [connected, setConnected] = useState(false);
  const latestRef = useRef<LatestAppShellEventContext>({
    activeSessionBackend,
    activeSessionHistorical,
    activeSessionId,
    activeSessionPending,
    activeSessionRuntimeId,
    bootstrapLoaded,
    items,
    onConnectionChange,
    refreshNotificationsFeed,
    workspaceOpen,
  });

  useEffect(() => {
    latestRef.current = {
      activeSessionBackend,
      activeSessionHistorical,
      activeSessionId,
      activeSessionPending,
      activeSessionRuntimeId,
      bootstrapLoaded,
      items,
      onConnectionChange,
      refreshNotificationsFeed,
      workspaceOpen,
    };
  }, [activeSessionBackend, activeSessionHistorical, activeSessionId, activeSessionPending, activeSessionRuntimeId, bootstrapLoaded, items, onConnectionChange, refreshNotificationsFeed, workspaceOpen]);

  useEffect(() => {
    const refreshSessions = () => sessionsStoreApi.refresh().catch(() => undefined);
    const refreshActiveWorkspace = () => {
      const latest = latestRef.current;
      if (!latest.activeSessionId || !latest.workspaceOpen) {
        return Promise.resolve();
      }
      if ((latest.activeSessionHistorical && latest.activeSessionBackend === "pi") || latest.activeSessionPending) {
        return Promise.resolve();
      }
      return (latest.activeSessionRuntimeId
        ? sessionUiStoreApi.refresh(latest.activeSessionId, { agentBackend: latest.activeSessionBackend, runtimeId: latest.activeSessionRuntimeId })
        : sessionUiStoreApi.refresh(latest.activeSessionId, { agentBackend: latest.activeSessionBackend }))
        .catch((error) => {
          if (isRecoverableNotFound(error)) {
            return refreshSessions();
          }
          return undefined;
        });
    };
    const refreshLiveSessionSnapshot = (sessionId: string, runtimeId?: string | null) => {
      return (runtimeId
        ? liveSessionStoreApi.poll(sessionId, runtimeId)
        : liveSessionStoreApi.poll(sessionId))
        .catch((error) => {
          if (isRecoverableNotFound(error)) {
            return refreshSessions();
          }
          return undefined;
        });
    };
    const updateConnectionState = (isOpen: boolean) => {
      setConnected(isOpen);
      latestRef.current.onConnectionChange?.(isOpen);
    };
    const handleFrame = (frame: RealtimeEnvelope) => {
      const type = String(frame.type || "").trim();
      if (!type) {
        return;
      }

      if (type === "sessions.updated") {
        void refreshSessions();
        return;
      }
      if (type === "notifications.invalidate") {
        void latestRef.current.refreshNotificationsFeed();
        return;
      }
      if (type === "session.workspace.invalidate") {
        void refreshActiveWorkspace();
        return;
      }
      if (type === "stream.resync") {
        void refreshSessions();
        void latestRef.current.refreshNotificationsFeed();
        if (latestRef.current.activeSessionId) {
          void refreshLiveSessionSnapshot(latestRef.current.activeSessionId, latestRef.current.activeSessionRuntimeId);
        }
        void refreshActiveWorkspace();
        return;
      }

      if (
        type === "session.state"
        || type === "session.generation.broken"
        || type === "message.delta"
        || type === "message.commit"
        || type === "ui.request"
        || type === "ui.resolved"
      ) {
        liveSessionStoreApi.applyFrame(frame);
        return;
      }

      if (type === "queue.state") {
        const sessionId = resolveSessionId(frame);
        const latest = latestRef.current;
        if (sessionId && latest.activeSessionId === sessionId && latest.workspaceOpen) {
          void refreshActiveWorkspace();
        }
        return;
      }

      if (type === "transport.reset_required") {
        const sessionId = resolveSessionId(frame);
        if (!sessionId) {
          return;
        }
        liveSessionStoreApi.applyFrame(frame);
        liveSessionStoreApi.resetSession(sessionId);
        const session = latestRef.current.items.find((item) => item.session_id === sessionId) ?? null;
        void refreshLiveSessionSnapshot(sessionId, session?.runtime_id ?? null);
        if (latestRef.current.activeSessionId === sessionId && latestRef.current.workspaceOpen) {
          void refreshActiveWorkspace();
        }
      }
    };

    const unsubscribeState = subscribeRealtimeState((next) => {
      updateConnectionState(next === "open");
    });
    const unsubscribeFrames = subscribeRealtimeFrames(handleFrame);

    if (bootstrapLoaded) {
      void connect().catch(() => undefined);
    }

    return () => {
      unsubscribeFrames();
      unsubscribeState();
      disconnect();
    };
  }, [bootstrapLoaded, liveSessionStoreApi, sessionUiStoreApi, sessionsStoreApi]);

  useEffect(() => {
    if (!bootstrapLoaded) {
      return;
    }
    const liveState = liveSessionStoreApi.getState();
    const subscriptions: RealtimeStreamSubscription[] = [{ name: "sessions" }];
    const trackedSessionIds = new Set<string>();
    const subscribeSession = (sessionId: string) => {
      if (!sessionId || trackedSessionIds.has(sessionId)) {
        return;
      }
      trackedSessionIds.add(sessionId);
      subscriptions.push({
        name: sessionStreamName(sessionId),
        resumeFrom: liveState.streamCursorsBySessionId[sessionId],
      });
      subscriptions.push({
        name: sessionUiStreamName(sessionId),
        resumeFrom: liveState.uiStreamCursorsBySessionId[sessionId],
      });
    };

    if (activeSessionId) {
      subscribeSession(activeSessionId);
    }
    for (const session of items) {
      if (session.busy || session.pending_startup) {
        subscribeSession(session.session_id);
      }
    }
    setRealtimeSubscriptions(subscriptions);
  }, [activeSessionId, bootstrapLoaded, items, liveSessionStoreApi]);

  return { connected };
}
