import { api } from "../../lib/api";
import { HttpError } from "../../lib/http";
import type {
  ContextUsagePayload,
  LiveSessionResponse,
  MessageEvent,
  RealtimeEnvelope,
  SessionTransportSnapshot,
  SessionUiRequest,
  TurnTimingPayload,
} from "../../lib/types";
import { DELTA_HISTORY_PAGE_SIZE, INITIAL_HISTORY_PAGE_SIZE } from "../messages/history";
import type { MessagesStore } from "../messages/store";

const EMPTY_INITIAL_HISTORY_RETRY_DELAYS_MS = [1000, 2000, 4000, 8000];

export interface LiveSessionState {
  offsetsBySessionId: Record<string, number>;
  liveOffsetsBySessionId: Record<string, number>;
  bridgeOffsetsBySessionId: Record<string, number>;
  streamCursorsBySessionId: Record<string, number>;
  uiStreamCursorsBySessionId: Record<string, number>;
  requestsBySessionId: Record<string, SessionUiRequest[]>;
  requestVersionsBySessionId: Record<string, string>;
  busyBySessionId: Record<string, boolean>;
  generatingBySessionId: Record<string, boolean>;
  loadingBySessionId: Record<string, boolean>;
  errorBySessionId: Record<string, string>;
  tokenBySessionId: Record<string, Record<string, unknown> | null>;
  contextUsageBySessionId: Record<string, ContextUsagePayload | null>;
  turnTimingBySessionId: Record<string, TurnTimingPayload | null>;
  runtimeStateBySessionId: Record<string, string | undefined>;
  runtimeStateReasonBySessionId: Record<string, string | undefined>;
  runtimeIdBySessionId: Record<string, string | undefined>;
  transportBySessionId: Record<string, SessionTransportSnapshot | null | undefined>;
}

export interface LiveSessionStore {
  getState(): LiveSessionState;
  subscribe(listener: () => void): () => void;
  loadInitial(sessionId: string, runtimeId?: string | null): Promise<void>;
  poll(sessionId: string, runtimeId?: string | null): Promise<void>;
  probe(sessionId: string, runtimeId?: string | null): Promise<void>;
  applyFrame(frame: RealtimeEnvelope): RealtimeFrameApplyResult;
  resetSession(sessionId: string): void;
  setBufferAssistantOutput(value: boolean): void;
}

export interface RealtimeFrameApplyResult {
  ignored?: boolean;
  reason?: "invalid_frame" | "stale_cursor" | "stale_runtime" | "stream_gap";
  resyncNeeded?: boolean;
  sessionId?: string;
  stream?: "session" | "ui";
  expectedSeq?: number;
  receivedSeq?: number;
}

function liveSessionErrorMessage(error: unknown): string {
  if (error instanceof HttpError && error.status === 404) {
    return "Pi RPC session ended or broker exited";
  }
  if (error instanceof Error && error.message.trim()) {
    return error.message;
  }
  return "live session unavailable";
}

function toObjectRecord(value: unknown) {
  return value && typeof value === "object" ? value as Record<string, unknown> : null;
}

function normalizeRequest(value: unknown): SessionUiRequest | null {
  const record = toObjectRecord(value);
  if (!record) {
    return null;
  }
  const requestId = typeof record.request_id === "string" && record.request_id.trim()
    ? record.request_id.trim()
    : typeof record.id === "string" && record.id.trim()
      ? record.id.trim()
      : "";
  return {
    ...record,
    ...(requestId ? { id: requestId } : {}),
  } as SessionUiRequest;
}

function resolveSessionId(frame: RealtimeEnvelope) {
  const payload = toObjectRecord(frame.payload);
  const payloadSessionId = typeof payload?.session_id === "string" && payload.session_id.trim()
    ? payload.session_id.trim()
    : "";
  if (payloadSessionId) {
    return payloadSessionId;
  }
  const stream = String(frame.stream || "").trim();
  if (!stream.startsWith("session:")) {
    return "";
  }
  const [, rest] = stream.split("session:");
  return String(rest || "").split(":")[0] || "";
}

function parseSnapshotCursor(value: unknown) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return Math.max(0, Math.floor(value));
  }
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value.trim());
    if (Number.isFinite(parsed)) {
      return Math.max(0, Math.floor(parsed));
    }
  }
  return undefined;
}

function nextCursor(prior: number | undefined, value: unknown) {
  const parsed = parseSnapshotCursor(value);
  if (parsed === undefined) {
    return prior ?? 0;
  }
  return Math.max(prior ?? 0, parsed);
}

function staleCursor(prior: number | undefined, value: unknown) {
  const parsed = parseSnapshotCursor(value);
  return parsed !== undefined && parsed <= (prior ?? 0);
}

function cursorGap(prior: number | undefined, value: unknown) {
  const parsed = parseSnapshotCursor(value);
  const baseline = typeof prior === "number" && Number.isFinite(prior) ? Math.floor(prior) : 0;
  if (parsed === undefined || baseline <= 0) {
    return null;
  }
  if (parsed <= baseline + 1) {
    return null;
  }
  return { expectedSeq: baseline + 1, receivedSeq: parsed };
}

function isCursorGapSensitiveMainFrame(type: string) {
  return type === "session.state"
    || type === "message.delta"
    || type === "message.generating"
    || type === "message.commit";
}

function assistantCommitEndsGeneration(message: Record<string, unknown> | null) {
  if (!message) {
    return true;
  }
  const role = typeof message.role === "string" ? message.role : "";
  if (role !== "assistant") {
    return false;
  }
  const details = toObjectRecord(message.details);
  const phase = typeof details?.phase === "string" ? details.phase.trim() : "";
  return phase === "" || phase === "final_answer";
}

function isUIStreamFrame(type: string) {
  return type === "ui.request" || type === "ui.resolved";
}

function normalizeMessageSnapshot(payload: Awaited<ReturnType<typeof api.listMessages>>) {
  const events = Array.isArray(payload.items)
    ? payload.items
    : Array.isArray(payload.events)
      ? payload.events
      : [];
  return {
    events,
    offset: typeof payload.offset === "number" ? payload.offset : typeof payload.tail_seq === "number" ? payload.tail_seq : undefined,
    hasOlder: payload.has_older === true || payload.has_more === true,
    nextBefore: typeof payload.next_before === "number"
      ? payload.next_before
      : typeof payload.next_before_seq === "number"
        ? payload.next_before_seq
        : undefined,
  };
}

function partialAssistantTurnEvent(payload: LiveSessionResponse, bufferAssistantOutput = false) {
  if (bufferAssistantOutput) {
    return [] as MessageEvent[];
  }
  const turn = toObjectRecord(payload.partial_assistant_turn);
  const turnId = typeof turn?.turn_id === "string" && turn.turn_id.trim() ? turn.turn_id.trim() : "";
  const text = typeof turn?.text === "string" ? turn.text : "";
  if (!turnId || !text) {
    return [] as MessageEvent[];
  }
  return [{
    role: "assistant",
    streaming: true,
    completed: false,
    stream_id: turnId,
    turn_id: turnId,
    text,
  }] as MessageEvent[];
}

function normalizeSnapshotEvents(messagePayload: Awaited<ReturnType<typeof api.listMessages>>, statePayload: LiveSessionResponse, bufferAssistantOutput = false) {
  return [...normalizeMessageSnapshot(messagePayload).events, ...partialAssistantTurnEvent(statePayload, bufferAssistantOutput)];
}

function transportSnapshotFromValue(value: unknown): SessionTransportSnapshot | null {
  const record = toObjectRecord(value);
  if (!record) {
    return null;
  }
  return {
    generation_id: typeof record.generation_id === "string" ? record.generation_id : undefined,
    state: typeof record.state === "string" ? record.state : undefined,
    reset_required: record.reset_required === true,
    reason: typeof record.reason === "string" ? record.reason : null,
  };
}

function transportSnapshotFromPayload(payload: LiveSessionResponse | Record<string, unknown> | null): SessionTransportSnapshot | null {
  const direct = transportSnapshotFromValue(payload && "transport" in payload ? payload.transport : null);
  if (direct) {
    return direct;
  }
  const state = payload && typeof payload.transport_state === "string" ? payload.transport_state : undefined;
  const generationId = payload && typeof payload.generation_id === "string" ? payload.generation_id : undefined;
  const reason = payload && typeof payload.transport_reason === "string"
    ? payload.transport_reason
    : payload && typeof payload.transport_error === "string"
      ? payload.transport_error
      : null;
  const resetRequired = payload?.reset_required === true;
  if (!state && !generationId && !reason && !resetRequired) {
    return null;
  }
  return {
    generation_id: generationId,
    state,
    reset_required: resetRequired,
    reason,
  };
}

function transportErrorMessage(payload: LiveSessionResponse | Record<string, unknown> | null) {
  const transport = transportSnapshotFromPayload(payload);
  if (!transport) {
    return "";
  }
  const reason = typeof transport.reason === "string" && transport.reason.trim() ? transport.reason.trim() : "";
  if (transport.reset_required) {
    return reason || "transport reset required";
  }
  if (transport.state === "broken") {
    return reason || "session generation broken";
  }
  return "";
}

function transportBusyValue(payload: LiveSessionResponse | Record<string, unknown> | null, busy: boolean) {
  const transport = transportSnapshotFromPayload(payload);
  if (transport?.state === "broken" || transport?.state === "failed" || transport?.state === "ended") {
    return false;
  }
  return busy;
}

function normalizedRuntimeState(payload: LiveSessionResponse | Record<string, unknown> | null) {
  return payload && typeof payload.runtime_state === "string" && payload.runtime_state.trim()
    ? payload.runtime_state.trim()
    : undefined;
}

function normalizedRuntimeStateReason(payload: LiveSessionResponse | Record<string, unknown> | null) {
  return payload && typeof payload.runtime_state_reason === "string" && payload.runtime_state_reason.trim()
    ? payload.runtime_state_reason.trim()
    : undefined;
}

function normalizedRuntimeId(payload: LiveSessionResponse | Record<string, unknown> | null) {
  return payload && typeof payload.runtime_id === "string" && payload.runtime_id.trim()
    ? payload.runtime_id.trim()
    : undefined;
}

function matchesKnownRuntime(state: LiveSessionState, sessionId: string, payload: Record<string, unknown> | null) {
  const frameRuntimeId = normalizedRuntimeId(payload);
  const knownRuntimeId = state.runtimeIdBySessionId[sessionId];
  return !frameRuntimeId || !knownRuntimeId || frameRuntimeId === knownRuntimeId;
}

function normalizeSnapshotRequests(payload: LiveSessionResponse) {
  if (Array.isArray(payload.requests)) {
    return payload.requests.map((request) => normalizeRequest(request)).filter((request): request is SessionUiRequest => request !== null);
  }
  const request = normalizeRequest(payload.ui_request);
  return request ? [request] : [];
}

export function createLiveSessionStore(messagesStore: MessagesStore): LiveSessionStore {
  let state: LiveSessionState = {
    offsetsBySessionId: {},
    liveOffsetsBySessionId: {},
    bridgeOffsetsBySessionId: {},
    streamCursorsBySessionId: {},
    uiStreamCursorsBySessionId: {},
    requestsBySessionId: {},
    requestVersionsBySessionId: {},
    busyBySessionId: {},
    generatingBySessionId: {},
    loadingBySessionId: {},
    errorBySessionId: {},
    tokenBySessionId: {},
    contextUsageBySessionId: {},
    turnTimingBySessionId: {},
    runtimeStateBySessionId: {},
    runtimeStateReasonBySessionId: {},
    runtimeIdBySessionId: {},
    transportBySessionId: {},
  };
  const listeners = new Set<() => void>();
  const inFlightBySessionId: Record<string, Promise<void> | undefined> = {};
  const queuedPollRuntimeBySessionId: Record<string, string | null | undefined> = {};
  const streamingTextBySessionId = new Map<string, Map<string, string>>();
  const streamingFlushTimersByStream = new Map<string, number>();
  const emptyInitialHistoryRetryTimersBySessionId: Record<string, number | undefined> = {};
  const emptyInitialHistoryRetryCountsBySessionId: Record<string, number> = {};
  let bufferAssistantOutput = false;

  const hasActiveAssistantOutput = (sessionId: string) => Boolean(
    (streamingTextBySessionId.get(sessionId)?.size ?? 0) > 0
    || messagesStore.hasStreamingAssistant(sessionId),
  );

  const emit = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  const clearEmptyInitialHistoryRetry = (sessionId: string) => {
    const timer = emptyInitialHistoryRetryTimersBySessionId[sessionId];
    if (timer !== undefined && typeof window !== "undefined") {
      window.clearTimeout(timer);
    }
    delete emptyInitialHistoryRetryTimersBySessionId[sessionId];
    delete emptyInitialHistoryRetryCountsBySessionId[sessionId];
  };

  const scheduleEmptyInitialHistoryRetry = (sessionId: string, runtimeId?: string | null) => {
    if (typeof window === "undefined") {
      return false;
    }
    if (emptyInitialHistoryRetryTimersBySessionId[sessionId] !== undefined) {
      return true;
    }
    const attempt = emptyInitialHistoryRetryCountsBySessionId[sessionId] ?? 0;
    if (attempt >= EMPTY_INITIAL_HISTORY_RETRY_DELAYS_MS.length) {
      return false;
    }
    const delay = EMPTY_INITIAL_HISTORY_RETRY_DELAYS_MS[attempt];
    emptyInitialHistoryRetryCountsBySessionId[sessionId] = attempt + 1;
    emptyInitialHistoryRetryTimersBySessionId[sessionId] = window.setTimeout(() => {
      delete emptyInitialHistoryRetryTimersBySessionId[sessionId];
      void runLoad(sessionId, true, runtimeId).catch(() => undefined);
    }, delay);
    return true;
  };

  const applyStatePayload = (
    sessionId: string,
    statePayload: LiveSessionResponse,
    offset?: number,
  ) => {
    const nextRequests = normalizeSnapshotRequests(statePayload);
    const nextRequestVersionsBySessionId = { ...state.requestVersionsBySessionId };
    if (typeof statePayload.requests_version === "string") {
      nextRequestVersionsBySessionId[sessionId] = statePayload.requests_version;
    }
    const busy = transportBusyValue(statePayload, statePayload.busy === true);
    const generating = busy && hasActiveAssistantOutput(sessionId);
    if (!generating) {
      clearStreamingFlushTimer(sessionId);
      streamingTextBySessionId.delete(sessionId);
      messagesStore.clearStreamingAssistant(sessionId);
    }
    state = {
      ...state,
      offsetsBySessionId: {
        ...state.offsetsBySessionId,
        [sessionId]: typeof offset === "number"
          ? offset
          : typeof statePayload.tail_seq === "number"
            ? Math.max(state.offsetsBySessionId[sessionId] ?? 0, statePayload.tail_seq)
            : state.offsetsBySessionId[sessionId] ?? 0,
      },
      liveOffsetsBySessionId: {
        ...state.liveOffsetsBySessionId,
        [sessionId]: 0,
      },
      bridgeOffsetsBySessionId: {
        ...state.bridgeOffsetsBySessionId,
        [sessionId]: 0,
      },
      streamCursorsBySessionId: {
        ...state.streamCursorsBySessionId,
        [sessionId]: nextCursor(
          state.streamCursorsBySessionId[sessionId],
          statePayload.resume_cursors?.session ?? statePayload.stream_cursors?.session ?? statePayload.stream_seq,
        ),
      },
      uiStreamCursorsBySessionId: {
        ...state.uiStreamCursorsBySessionId,
        [sessionId]: nextCursor(
          state.uiStreamCursorsBySessionId[sessionId],
          statePayload.resume_cursors?.ui ?? statePayload.stream_cursors?.ui ?? statePayload.ui_stream_seq,
        ),
      },
      requestsBySessionId: {
        ...state.requestsBySessionId,
        [sessionId]: nextRequests,
      },
      requestVersionsBySessionId: nextRequestVersionsBySessionId,
      busyBySessionId: {
        ...state.busyBySessionId,
        [sessionId]: busy,
      },
      generatingBySessionId: {
        ...state.generatingBySessionId,
        [sessionId]: generating,
      },
      loadingBySessionId: {
        ...state.loadingBySessionId,
        [sessionId]: false,
      },
      errorBySessionId: {
        ...state.errorBySessionId,
        [sessionId]: transportErrorMessage(statePayload),
      },
      tokenBySessionId: {
        ...state.tokenBySessionId,
        [sessionId]: toObjectRecord(statePayload.token),
      },
      contextUsageBySessionId: {
        ...state.contextUsageBySessionId,
        [sessionId]: toObjectRecord(statePayload.context_usage) as ContextUsagePayload | null,
      },
      turnTimingBySessionId: {
        ...state.turnTimingBySessionId,
        [sessionId]: toObjectRecord(statePayload.turn_timing) as TurnTimingPayload | null,
      },
      runtimeStateBySessionId: {
        ...state.runtimeStateBySessionId,
        [sessionId]: normalizedRuntimeState(statePayload),
      },
      runtimeStateReasonBySessionId: {
        ...state.runtimeStateReasonBySessionId,
        [sessionId]: normalizedRuntimeStateReason(statePayload),
      },
      runtimeIdBySessionId: {
        ...state.runtimeIdBySessionId,
        [sessionId]: normalizedRuntimeId(statePayload) ?? state.runtimeIdBySessionId[sessionId],
      },
      transportBySessionId: {
        ...state.transportBySessionId,
        [sessionId]: transportSnapshotFromPayload(statePayload),
      },
    };
    emit();
  };

  const applySnapshot = (
    sessionId: string,
    messagePayload: Awaited<ReturnType<typeof api.listMessages>>,
    statePayload: LiveSessionResponse,
    replace: boolean,
    loaded = true,
  ) => {
    const normalizedMessages = normalizeMessageSnapshot(messagePayload);
    const snapshotEvents = normalizeSnapshotEvents(messagePayload, statePayload, bufferAssistantOutput);
    messagesStore.applySnapshot(sessionId, snapshotEvents, {
      offset: normalizedMessages.offset,
      hasOlder: normalizedMessages.hasOlder,
      nextBefore: normalizedMessages.nextBefore,
      replace,
      loaded,
    });
    applyStatePayload(sessionId, statePayload, normalizedMessages.offset);
    if (snapshotEvents.length > 0) {
      clearEmptyInitialHistoryRetry(sessionId);
    }
    return snapshotEvents.length;
  };

  const stateProbeNeedsMessages = (sessionId: string, statePayload: LiveSessionResponse) => {
    if (typeof statePayload.tail_seq !== "number" || !Number.isFinite(statePayload.tail_seq)) {
      return true;
    }
    const currentOffset = state.offsetsBySessionId[sessionId];
    if (typeof currentOffset !== "number" || !Number.isFinite(currentOffset)) {
      return true;
    }
    return Math.floor(statePayload.tail_seq) !== Math.floor(currentOffset);
  };

  const runLoad = async (sessionId: string, replace: boolean, runtimeId?: string | null): Promise<void> => {
    const existing = inFlightBySessionId[sessionId];
    if (existing) {
      if (!replace) {
        queuedPollRuntimeBySessionId[sessionId] = runtimeId ?? null;
        return existing.then((): Promise<void> | undefined => {
          if (!(sessionId in queuedPollRuntimeBySessionId)) {
            return undefined;
          }
          const queuedRuntimeId = queuedPollRuntimeBySessionId[sessionId];
          delete queuedPollRuntimeBySessionId[sessionId];
          return runLoad(sessionId, false, queuedRuntimeId);
        });
      }
      return existing;
    }

    const request = (async () => {
      state = {
        ...state,
        loadingBySessionId: {
          ...state.loadingBySessionId,
          [sessionId]: true,
        },
      };
      emit();

      try {
        const loadState = () => typeof (api as { getSessionState?: unknown }).getSessionState === "function"
          ? runtimeId
            ? (api as { getSessionState(sessionId: string, signal?: AbortSignal, runtimeId?: string | null): Promise<LiveSessionResponse> }).getSessionState(sessionId, undefined, runtimeId)
            : (api as { getSessionState(sessionId: string, signal?: AbortSignal, runtimeId?: string | null): Promise<LiveSessionResponse> }).getSessionState(sessionId)
          : runtimeId
            ? (api as { getLiveSession(sessionId: string, offset?: number, requestsVersion?: string, signal?: AbortSignal, liveOffset?: number, runtimeId?: string | null, bridgeOffset?: number): Promise<LiveSessionResponse> }).getLiveSession(sessionId, state.offsetsBySessionId[sessionId], undefined, undefined, undefined, runtimeId, undefined)
            : (api as { getLiveSession(sessionId: string, offset?: number, requestsVersion?: string, signal?: AbortSignal, liveOffset?: number, runtimeId?: string | null, bridgeOffset?: number): Promise<LiveSessionResponse> }).getLiveSession(sessionId, state.offsetsBySessionId[sessionId]);

        const hasKnownOffset = typeof state.offsetsBySessionId[sessionId] === "number" && Number.isFinite(state.offsetsBySessionId[sessionId]);
        if (!replace && hasKnownOffset) {
          const statePayload = await loadState();
          if (!stateProbeNeedsMessages(sessionId, statePayload)) {
            applyStatePayload(sessionId, statePayload);
            return;
          }
          const currentOffset = state.offsetsBySessionId[sessionId];
          const safeAfter = currentOffset > 0 ? currentOffset : undefined;
          const pageSize = safeAfter === undefined ? INITIAL_HISTORY_PAGE_SIZE : DELTA_HISTORY_PAGE_SIZE;
          const messagePayload = runtimeId
            ? await api.listMessages(sessionId, safeAfter === undefined, undefined, safeAfter, undefined, pageSize, runtimeId, true)
            : await api.listMessages(sessionId, safeAfter === undefined, undefined, safeAfter, undefined, pageSize, undefined, true);
          applySnapshot(sessionId, messagePayload, statePayload, false);
          return;
        }

        const messageInit = replace || !hasKnownOffset;
        const [messagePayload, statePayload] = await Promise.all([
          runtimeId
            ? api.listMessages(sessionId, messageInit, undefined, undefined, undefined, INITIAL_HISTORY_PAGE_SIZE, runtimeId, true)
            : api.listMessages(sessionId, messageInit, undefined, undefined, undefined, INITIAL_HISTORY_PAGE_SIZE, undefined, true),
          loadState(),
        ]);
        const loaded = !replace || normalizeSnapshotEvents(messagePayload, statePayload, bufferAssistantOutput).length > 0 || !scheduleEmptyInitialHistoryRetry(sessionId, runtimeId);
        applySnapshot(sessionId, messagePayload, statePayload, replace, loaded);
        if (replace && loaded) {
          clearEmptyInitialHistoryRetry(sessionId);
        }
      } catch (error) {
        const message = liveSessionErrorMessage(error);
        state = {
          ...state,
          loadingBySessionId: {
            ...state.loadingBySessionId,
            [sessionId]: false,
          },
          errorBySessionId: {
            ...state.errorBySessionId,
            [sessionId]: message,
          },
        };
        emit();
        throw error;
      } finally {
        delete inFlightBySessionId[sessionId];
      }
    })();

    inFlightBySessionId[sessionId] = request;
    return request;
  };

  const appendRealtimeEvent = (sessionId: string, event: MessageEvent) => {
    messagesStore.applyLive(sessionId, [event], {
      replace: false,
      offset: state.offsetsBySessionId[sessionId],
      hasOlder: state.offsetsBySessionId[sessionId] !== undefined ? state.offsetsBySessionId[sessionId] > 0 : undefined,
    });
    clearEmptyInitialHistoryRetry(sessionId);
  };
  const appendStreamingAssistantEvent = (sessionId: string, turnId: string, frame: RealtimeEnvelope, payload: Record<string, unknown>, text: string) => {
    appendRealtimeEvent(sessionId, {
      event_id: typeof frame.id === "string" ? frame.id : undefined,
      role: typeof payload.role === "string" ? payload.role : "assistant",
      streaming: true,
      completed: false,
      stream_id: turnId,
      turn_id: turnId,
      text,
      ts: typeof frame.ts === "number" ? frame.ts : undefined,
    });
  };
  const streamingFlushTimerKey = (sessionId: string, turnId: string) => `${sessionId}\u0001${turnId}`;
  const clearStreamingFlushTimer = (sessionId: string, turnId = "") => {
    const keys = turnId
      ? [streamingFlushTimerKey(sessionId, turnId)]
      : [...streamingFlushTimersByStream.keys()].filter((key) => key.startsWith(`${sessionId}\u0001`));
    for (const key of keys) {
      const timer = streamingFlushTimersByStream.get(key);
      if (timer !== undefined && typeof window !== "undefined") {
        window.clearTimeout(timer);
      }
      streamingFlushTimersByStream.delete(key);
    }
  };
  const clearStreamingArtifacts = (sessionId: string) => {
    clearStreamingFlushTimer(sessionId);
    streamingTextBySessionId.delete(sessionId);
    messagesStore.clearStreamingAssistant(sessionId);
  };
  const scheduleStreamingFlush = (sessionId: string, turnId: string, frame: RealtimeEnvelope, payload: Record<string, unknown>, text: string) => {
    if (typeof window === "undefined") {
      appendStreamingAssistantEvent(sessionId, turnId, frame, payload, text);
      return;
    }
    const key = streamingFlushTimerKey(sessionId, turnId);
    if (streamingFlushTimersByStream.has(key)) {
      return;
    }
    const timer = window.setTimeout(() => {
      streamingFlushTimersByStream.delete(key);
      const latestText = streamingTextBySessionId.get(sessionId)?.get(turnId) ?? text;
      appendStreamingAssistantEvent(sessionId, turnId, frame, payload, latestText);
    }, 120);
    streamingFlushTimersByStream.set(key, timer);
  };

  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    loadInitial(sessionId: string, runtimeId?: string | null) {
      return runLoad(sessionId, true, runtimeId);
    },
    poll(sessionId: string, runtimeId?: string | null) {
      return runLoad(sessionId, false, runtimeId);
    },
    async probe(sessionId: string, runtimeId?: string | null) {
      const response = runtimeId
        ? await api.probeSessionState(sessionId, runtimeId)
        : await api.probeSessionState(sessionId);
      applyStatePayload(sessionId, response.state);
      if (typeof window !== "undefined") {
        window.setTimeout(() => {
          void runLoad(sessionId, false, runtimeId);
        }, 500);
      }
    },
    applyFrame(frame: RealtimeEnvelope) {
      const type = String(frame.type || "").trim();
      const sessionId = resolveSessionId(frame);
      const payload = toObjectRecord(frame.payload);
      if (!type || !sessionId) {
        return { ignored: true, reason: "invalid_frame" };
      }
      const mainGap = isCursorGapSensitiveMainFrame(type) ? cursorGap(state.streamCursorsBySessionId[sessionId], payload?.stream_seq) : null;
      if (mainGap) {
        return {
          ignored: true,
          reason: "stream_gap",
          resyncNeeded: true,
          sessionId,
          stream: "session",
          expectedSeq: mainGap.expectedSeq,
          receivedSeq: mainGap.receivedSeq,
        };
      }
      const uiGap = isUIStreamFrame(type) ? cursorGap(state.uiStreamCursorsBySessionId[sessionId], payload?.stream_seq) : null;
      if (uiGap) {
        return {
          ignored: true,
          reason: "stream_gap",
          resyncNeeded: true,
          sessionId,
          stream: "ui",
          expectedSeq: uiGap.expectedSeq,
          receivedSeq: uiGap.receivedSeq,
        };
      }
      if (isCursorGapSensitiveMainFrame(type) && staleCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq)) {
        return { ignored: true, reason: "stale_cursor", sessionId, stream: "session" };
      }
      if (isUIStreamFrame(type) && staleCursor(state.uiStreamCursorsBySessionId[sessionId], payload?.stream_seq)) {
        return { ignored: true, reason: "stale_cursor", sessionId, stream: "ui" };
      }

      if (type === "session.state") {
        const busy = transportBusyValue(payload, payload?.busy === true);
        const generating = busy && hasActiveAssistantOutput(sessionId);
        if (!generating) {
          clearStreamingArtifacts(sessionId);
        }
        state = {
          ...state,
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          busyBySessionId: {
            ...state.busyBySessionId,
            [sessionId]: busy,
          },
          generatingBySessionId: {
            ...state.generatingBySessionId,
            [sessionId]: generating,
          },
          errorBySessionId: {
            ...state.errorBySessionId,
            [sessionId]: transportErrorMessage(payload),
          },
          runtimeStateBySessionId: {
            ...state.runtimeStateBySessionId,
            [sessionId]: normalizedRuntimeState(payload),
          },
          runtimeStateReasonBySessionId: {
            ...state.runtimeStateReasonBySessionId,
            [sessionId]: normalizedRuntimeStateReason(payload),
          },
          runtimeIdBySessionId: {
            ...state.runtimeIdBySessionId,
            [sessionId]: normalizedRuntimeId(payload) ?? state.runtimeIdBySessionId[sessionId],
          },
          transportBySessionId: {
            ...state.transportBySessionId,
            [sessionId]: transportSnapshotFromPayload(payload) ?? state.transportBySessionId[sessionId],
          },
        };
        emit();
        return {};
      }

      if (type === "message.delta") {
        const backendBusy = state.busyBySessionId[sessionId];
        if (backendBusy === false) {
          clearStreamingArtifacts(sessionId);
          state = {
            ...state,
            streamCursorsBySessionId: {
              ...state.streamCursorsBySessionId,
              [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
            },
          };
          emit();
          return {};
        }
        const turnId = typeof payload?.turn_id === "string" && payload.turn_id.trim()
          ? payload.turn_id.trim()
          : String(frame.id || `stream_${Date.now()}`);
        const perSession = streamingTextBySessionId.get(sessionId) ?? new Map<string, string>();
        const previous = perSession.get(turnId) || "";
        const delta = typeof payload?.delta === "string" ? payload.delta : "";
        const nextText = previous + delta;
        perSession.set(turnId, nextText);
        streamingTextBySessionId.set(sessionId, perSession);
        if (!bufferAssistantOutput) {
          scheduleStreamingFlush(sessionId, turnId, frame, payload ?? {}, nextText);
        }
        state = {
          ...state,
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          generatingBySessionId: backendBusy === true
            ? {
                ...state.generatingBySessionId,
                [sessionId]: true,
              }
            : state.generatingBySessionId,
        };
        emit();
        return {};
      }

      if (type === "message.generating") {
        const active = payload?.active === true;
        const role = typeof payload?.role === "string" ? payload.role.trim() : "";
        const assistantGeneration = role === "assistant";
        state = {
          ...state,
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          generatingBySessionId: assistantGeneration && state.busyBySessionId[sessionId] === true
            ? {
                ...state.generatingBySessionId,
                [sessionId]: active,
              }
            : state.generatingBySessionId,
        };
        emit();
        return {};
      }

      if (type === "message.commit") {
        const turnId = typeof payload?.turn_id === "string" && payload.turn_id.trim()
          ? payload.turn_id.trim()
          : "";
        const message = toObjectRecord(payload?.message);
        const role = typeof message?.role === "string" ? message.role : typeof payload?.role === "string" ? payload.role : undefined;
        const assistantFinal = assistantCommitEndsGeneration(message);
        if (assistantFinal) {
          clearStreamingFlushTimer(sessionId, turnId);
          if (turnId) {
            streamingTextBySessionId.get(sessionId)?.delete(turnId);
          }
        }
        appendRealtimeEvent(sessionId, {
          ...message,
          event_id: typeof message?.event_id === "string" ? message.event_id : undefined,
          turn_id: turnId || (typeof message?.turn_id === "string" ? message.turn_id : undefined),
          role,
          text: typeof message?.text === "string" ? message.text : undefined,
          ts: typeof message?.ts === "number" ? message.ts : typeof frame.ts === "number" ? frame.ts : undefined,
        } as MessageEvent);
        state = {
          ...state,
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          generatingBySessionId: {
            ...state.generatingBySessionId,
            [sessionId]: assistantFinal ? false : state.generatingBySessionId[sessionId] === true,
          },
        };
        emit();
        return {};
      }

      if (type === "ui.request") {
        const request = normalizeRequest(payload?.request);
        if (!request) {
          return { ignored: true, reason: "invalid_frame", sessionId, stream: "ui" };
        }
        const prior = state.requestsBySessionId[sessionId] ?? [];
        const next = request.id
          ? [...prior.filter((item) => item.id !== request.id), request]
          : [...prior, request];
        state = {
          ...state,
          uiStreamCursorsBySessionId: {
            ...state.uiStreamCursorsBySessionId,
            [sessionId]: nextCursor(state.uiStreamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          requestsBySessionId: {
            ...state.requestsBySessionId,
            [sessionId]: next,
          },
        };
        emit();
        return {};
      }

      if (type === "ui.resolved") {
        const requestId = typeof payload?.request_id === "string" && payload.request_id.trim()
          ? payload.request_id.trim()
          : typeof payload?.response_to === "string" && payload.response_to.trim()
            ? payload.response_to.trim()
            : "";
        if (!requestId) {
          return { ignored: true, reason: "invalid_frame", sessionId, stream: "ui" };
        }
        state = {
          ...state,
          uiStreamCursorsBySessionId: {
            ...state.uiStreamCursorsBySessionId,
            [sessionId]: nextCursor(state.uiStreamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          requestsBySessionId: {
            ...state.requestsBySessionId,
            [sessionId]: (state.requestsBySessionId[sessionId] ?? []).filter((request) => request.id !== requestId),
          },
        };
        emit();
        return {};
      }

      if (type === "session.generation.broken") {
        if (!matchesKnownRuntime(state, sessionId, payload)) {
          return { ignored: true, reason: "stale_runtime", sessionId, stream: "session" };
        }
        clearStreamingArtifacts(sessionId);
        state = {
          ...state,
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          busyBySessionId: {
            ...state.busyBySessionId,
            [sessionId]: false,
          },
          generatingBySessionId: {
            ...state.generatingBySessionId,
            [sessionId]: false,
          },
          errorBySessionId: {
            ...state.errorBySessionId,
            [sessionId]: typeof payload?.reason === "string" && payload.reason.trim()
              ? payload.reason
              : "session generation broken",
          },
          runtimeStateBySessionId: {
            ...state.runtimeStateBySessionId,
            [sessionId]: "failed",
          },
          transportBySessionId: {
            ...state.transportBySessionId,
            [sessionId]: {
              state: "broken",
              reason: typeof payload?.reason === "string" ? payload.reason : undefined,
              reset_required: false,
            },
          },
        };
        emit();
        return {};
      }

      if (type === "transport.reset_required") {
        if (!matchesKnownRuntime(state, sessionId, payload)) {
          return { ignored: true, reason: "stale_runtime", sessionId, stream: "session" };
        }
        clearStreamingArtifacts(sessionId);
        state = {
          ...state,
          errorBySessionId: {
            ...state.errorBySessionId,
            [sessionId]: typeof payload?.reason === "string" && payload.reason.trim()
              ? payload.reason
              : "transport reset required",
          },
          busyBySessionId: {
            ...state.busyBySessionId,
            [sessionId]: false,
          },
          generatingBySessionId: {
            ...state.generatingBySessionId,
            [sessionId]: false,
          },
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: 0,
          },
          uiStreamCursorsBySessionId: {
            ...state.uiStreamCursorsBySessionId,
            [sessionId]: 0,
          },
          runtimeStateBySessionId: {
            ...state.runtimeStateBySessionId,
            [sessionId]: "failed",
          },
          transportBySessionId: {
            ...state.transportBySessionId,
            [sessionId]: {
              state: "broken",
              reason: typeof payload?.reason === "string" ? payload.reason : undefined,
              reset_required: true,
            },
          },
        };
        emit();
        return {};
      }
      return { ignored: true, reason: "invalid_frame", sessionId };
    },
    resetSession(sessionId: string) {
      clearStreamingArtifacts(sessionId);
      state = {
        ...state,
        streamCursorsBySessionId: {
          ...state.streamCursorsBySessionId,
          [sessionId]: 0,
        },
        uiStreamCursorsBySessionId: {
          ...state.uiStreamCursorsBySessionId,
          [sessionId]: 0,
        },
        requestsBySessionId: {
          ...state.requestsBySessionId,
          [sessionId]: [],
        },
        generatingBySessionId: {
          ...state.generatingBySessionId,
          [sessionId]: false,
        },
        runtimeStateBySessionId: {
          ...state.runtimeStateBySessionId,
          [sessionId]: state.runtimeStateBySessionId[sessionId] === "failed" || state.runtimeStateBySessionId[sessionId] === "ended"
            ? state.runtimeStateBySessionId[sessionId]
            : undefined,
        },
        runtimeStateReasonBySessionId: {
          ...state.runtimeStateReasonBySessionId,
          [sessionId]: undefined,
        },
        runtimeIdBySessionId: {
          ...state.runtimeIdBySessionId,
          [sessionId]: undefined,
        },
        transportBySessionId: {
          ...state.transportBySessionId,
          [sessionId]: undefined,
        },
      };
      emit();
    },
    setBufferAssistantOutput(value: boolean) {
      bufferAssistantOutput = value;
    },
  };
}
