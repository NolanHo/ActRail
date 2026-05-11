import { useState } from "preact/hooks";

import { Button } from "@/components/ui/button";

import { useSessionUiStore, useSessionUiStoreApi, useSessionsStore, useSessionsStoreApi } from "../../app/providers";
import { api } from "../../lib/api";
import { getSessionRuntimeId } from "../../lib/session-identity";

function queueItemsFromValue(queue: Record<string, unknown> | null) {
  const rawItems = queue?.items;
  if (!Array.isArray(rawItems)) {
    return [] as Array<{ id: string; text: string }>;
  }
  return rawItems
    .map((item, index) => {
      if (item && typeof item === "object") {
        const record = item as { id?: unknown; queue_id?: unknown; text?: unknown };
        return {
          id: String(record.id ?? record.queue_id ?? index),
          text: String(record.text ?? ""),
        };
      }
      return { id: String(index), text: String(item) };
    })
    .filter((item) => item.text.trim().length > 0);
}

function queueCountFromSession(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return 0;
  }
  return Math.max(0, Math.round(value));
}

export function ConversationStateTray() {
  const { activeSessionId, items } = useSessionsStore();
  const { sessionId: sessionUiSessionId, runtimeId: sessionUiRuntimeId, queue } = useSessionUiStore();
  const sessionsStoreApi = useSessionsStoreApi();
  const sessionUiStoreApi = useSessionUiStoreApi();
  const [cancelling, setCancelling] = useState(false);
  const activeSession = items.find((session) => session.session_id === activeSessionId) ?? null;
  const activeRuntimeId = getSessionRuntimeId(activeSession);
  const queueMatchesActive = Boolean(
    activeSessionId
      && (
        sessionUiSessionId === activeSessionId
        || (
          typeof sessionUiRuntimeId === "string"
          && sessionUiRuntimeId.trim().length > 0
          && typeof activeRuntimeId === "string"
          && activeRuntimeId.trim().length > 0
          && sessionUiRuntimeId === activeRuntimeId
        )
      ),
  );
  const queueItems = queueMatchesActive ? queueItemsFromValue(queue) : [];
  const queueCount = Math.max(queueItems.length, queueCountFromSession(activeSession?.queue_len));

  if (!activeSessionId || queueCount <= 0) {
    return null;
  }

  const cancelQueue = async () => {
    if (!activeSessionId || cancelling) {
      return;
    }
    setCancelling(true);
    try {
      if (activeRuntimeId) {
        await api.cancelQueue(activeSessionId, activeRuntimeId);
      } else {
        await api.cancelQueue(activeSessionId);
      }
      await Promise.allSettled([
        sessionsStoreApi.refresh(),
        activeRuntimeId
          ? sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSession?.agent_backend, runtimeId: activeRuntimeId })
          : sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSession?.agent_backend }),
      ]);
    } finally {
      setCancelling(false);
    }
  };

  return (
    <section className="conversationStateTray" data-testid="conversation-state-tray" aria-label="Inbox instructions">
      <div className="conversationQueueTray">
        <div className="conversationQueueTrayHeader">
          <div>
            <h2>Inbox</h2>
            <p>{queueCount} inbox instruction{queueCount === 1 ? "" : "s"}</p>
          </div>
          <Button type="button" variant="outline" size="sm" disabled={cancelling} onClick={() => { void cancelQueue(); }}>
            {cancelling ? "Removing..." : "Remove from Inbox"}
          </Button>
        </div>
        {queueItems.length ? (
          <ol className="conversationQueueTrayList">
            {queueItems.slice(0, 3).map((item) => (
              <li key={item.id}>{item.text}</li>
            ))}
            {queueItems.length > 3 ? <li className="conversationQueueTrayMore">{queueItems.length - 3} more</li> : null}
          </ol>
        ) : (
          <p className="conversationQueueTrayFallback">Inbox instructions are waiting for a controllable runtime.</p>
        )}
      </div>
    </section>
  );
}
