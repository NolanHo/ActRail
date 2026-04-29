import { api } from "../../lib/api";
import type { ActiveWaitSummary, RealtimeEnvelope, WaitRecord, WaitThreadSummary } from "../../lib/types";
import { isActiveWaitState, normalizeActiveWait, normalizeThread, normalizeWait } from "./normalize";

export interface WaitsState {
  activeBySessionId: Record<string, ActiveWaitSummary | null>;
  inbox: ActiveWaitSummary[];
  threadsBySessionId: Record<string, WaitThreadSummary[]>;
  waitsByThreadId: Record<string, WaitRecord[]>;
  selectedThreadBySessionId: Record<string, string | null>;
  loading: boolean;
  error: string;
}

export interface WaitsStore {
  getState(): WaitsState;
  subscribe(listener: () => void): () => void;
  applyFrame(frame: RealtimeEnvelope): void;
  loadInbox(): Promise<void>;
  loadThreads(sessionId: string, runtimeId?: string | null): Promise<void>;
  loadThread(sessionId: string, threadId: string, runtimeId?: string | null): Promise<void>;
  selectThread(sessionId: string, threadId: string | null): void;
  openWait(wait: ActiveWaitSummary): void;
  claimWait(sessionId: string, waitId: string, runtimeId?: string | null): Promise<void>;
  answerWait(sessionId: string, waitId: string, answer: string, runtimeId?: string | null): Promise<void>;
  cancelWait(sessionId: string, waitId: string, runtimeId?: string | null): Promise<void>;
}

function payloadRecord(frame: RealtimeEnvelope) {
  return frame.payload && typeof frame.payload === "object" ? frame.payload as Record<string, unknown> : null;
}

function frameSessionId(frame: RealtimeEnvelope) {
  const payload = payloadRecord(frame);
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

function removeActive(inbox: ActiveWaitSummary[], sessionId: string, waitId?: string) {
  return inbox.filter((wait) => {
    if (waitId && wait.wait_id === waitId) {
      return false;
    }
    return wait.session_id !== sessionId;
  });
}

function upsertActive(inbox: ActiveWaitSummary[], wait: ActiveWaitSummary) {
  const sessionId = wait.session_id || "";
  return [wait, ...inbox.filter((item) => item.wait_id !== wait.wait_id && (!sessionId || item.session_id !== sessionId))];
}

function errorMessage(error: unknown) {
  return error instanceof Error && error.message.trim() ? error.message : "wait request failed";
}

export function createWaitsStore(): WaitsStore {
  let state: WaitsState = {
    activeBySessionId: {},
    inbox: [],
    threadsBySessionId: {},
    waitsByThreadId: {},
    selectedThreadBySessionId: {},
    loading: false,
    error: "",
  };
  const listeners = new Set<() => void>();

  const emit = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  const setActiveWait = (sessionId: string, wait: ActiveWaitSummary | null) => {
    if (!sessionId) {
      return;
    }
    state = {
      ...state,
      activeBySessionId: {
        ...state.activeBySessionId,
        [sessionId]: wait,
      },
      inbox: wait ? upsertActive(state.inbox, wait) : removeActive(state.inbox, sessionId),
    };
  };

  const applyWaitPayload = (rawWait: unknown, fallbackSessionId = "") => {
    const wait = normalizeActiveWait(rawWait, fallbackSessionId);
    if (!wait) {
      return;
    }
    const sessionId = wait.session_id || fallbackSessionId;
    if (!sessionId) {
      return;
    }
    if (isActiveWaitState(wait.state)) {
      setActiveWait(sessionId, { ...wait, session_id: sessionId });
    } else {
      setActiveWait(sessionId, null);
      state = {
        ...state,
        inbox: removeActive(state.inbox, sessionId, wait.wait_id),
      };
    }
  };

  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    applyFrame(frame: RealtimeEnvelope) {
      const type = String(frame.type || "").trim();
      const payload = payloadRecord(frame);
      const sessionId = frameSessionId(frame);
      if (!type || !payload) {
        return;
      }
      if (type === "session.state") {
        const wait = normalizeActiveWait(payload.active_wait, sessionId);
        setActiveWait(sessionId, wait ? { ...wait, session_id: wait.session_id || sessionId } : null);
        emit();
        return;
      }
      if (type.startsWith("wait.")) {
        applyWaitPayload(payload.wait ?? payload.active_wait ?? payload, sessionId);
        emit();
        return;
      }
      if (type === "waits.updated") {
        const waits = Array.isArray(payload.waits)
          ? payload.waits.map((item) => normalizeActiveWait(item)).filter((item): item is ActiveWaitSummary => Boolean(item && item.session_id))
          : [];
        if (waits.length) {
          const nextActive = { ...state.activeBySessionId };
          for (const wait of waits) {
            if (wait.session_id && isActiveWaitState(wait.state)) {
              nextActive[wait.session_id] = wait;
            }
          }
          state = {
            ...state,
            activeBySessionId: nextActive,
            inbox: waits.filter((wait) => isActiveWaitState(wait.state)),
          };
          emit();
        }
      }
    },
    async loadInbox() {
      state = { ...state, loading: true, error: "" };
      emit();
      try {
        const response = await api.getWaitInbox();
        const waits = Array.isArray(response.waits)
          ? response.waits.map((item) => normalizeActiveWait(item)).filter((item): item is ActiveWaitSummary => Boolean(item && item.session_id && isActiveWaitState(item.state)))
          : [];
        const activeBySessionId = { ...state.activeBySessionId };
        for (const wait of waits) {
          if (wait.session_id) {
            activeBySessionId[wait.session_id] = wait;
          }
        }
        state = { ...state, inbox: waits, activeBySessionId, loading: false };
        emit();
      } catch (error) {
        state = { ...state, loading: false, error: errorMessage(error) };
        emit();
      }
    },
    async loadThreads(sessionId: string, runtimeId?: string | null) {
      if (!sessionId) {
        return;
      }
      state = { ...state, loading: true, error: "" };
      emit();
      try {
        const response = await api.getWaitThreads(sessionId, undefined, runtimeId);
        const threads = Array.isArray(response.threads)
          ? response.threads.map((item) => normalizeThread(item, sessionId)).filter((item): item is WaitThreadSummary => Boolean(item))
          : [];
        state = {
          ...state,
          threadsBySessionId: { ...state.threadsBySessionId, [sessionId]: threads },
          loading: false,
        };
        emit();
      } catch (error) {
        state = { ...state, loading: false, error: errorMessage(error) };
        emit();
      }
    },
    async loadThread(sessionId: string, threadId: string, runtimeId?: string | null) {
      if (!sessionId || !threadId) {
        return;
      }
      state = { ...state, loading: true, error: "" };
      emit();
      try {
        const response = await api.getWaitThread(sessionId, threadId, undefined, runtimeId);
        const waits = Array.isArray(response.waits)
          ? response.waits.map((item) => normalizeWait(item)).filter((item): item is WaitRecord => Boolean(item))
          : [];
        const thread = normalizeThread(response.thread, sessionId);
        const existingThreads = state.threadsBySessionId[sessionId] ?? [];
        const nextThreads = thread
          ? [thread, ...existingThreads.filter((item) => item.thread_id !== thread.thread_id)]
          : existingThreads;
        state = {
          ...state,
          selectedThreadBySessionId: { ...state.selectedThreadBySessionId, [sessionId]: threadId },
          threadsBySessionId: { ...state.threadsBySessionId, [sessionId]: nextThreads },
          waitsByThreadId: { ...state.waitsByThreadId, [threadId]: waits },
          loading: false,
        };
        emit();
      } catch (error) {
        state = { ...state, loading: false, error: errorMessage(error) };
        emit();
      }
    },
    selectThread(sessionId: string, threadId: string | null) {
      state = {
        ...state,
        selectedThreadBySessionId: { ...state.selectedThreadBySessionId, [sessionId]: threadId },
      };
      emit();
    },
    openWait(wait: ActiveWaitSummary) {
      if (!wait.session_id) {
        return;
      }
      setActiveWait(wait.session_id, wait);
      state = {
        ...state,
        selectedThreadBySessionId: { ...state.selectedThreadBySessionId, [wait.session_id]: wait.thread_id },
      };
      emit();
    },
    async claimWait(sessionId: string, waitId: string, runtimeId?: string | null) {
      const response = await api.claimWait(sessionId, waitId, runtimeId);
      applyWaitPayload(response.wait ?? response.active_wait, sessionId);
      emit();
    },
    async answerWait(sessionId: string, waitId: string, answer: string, runtimeId?: string | null) {
      const response = await api.answerWait(sessionId, waitId, answer, runtimeId);
      applyWaitPayload(response.wait ?? response.active_wait, sessionId);
      emit();
    },
    async cancelWait(sessionId: string, waitId: string, runtimeId?: string | null) {
      const response = await api.cancelWait(sessionId, waitId, runtimeId);
      applyWaitPayload(response.wait ?? response.active_wait, sessionId);
      emit();
    },
  };
}
