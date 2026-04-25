import { api } from "../../lib/api";
import { HttpError } from "../../lib/http";
import type {
  ContextUsagePayload,
  LiveSessionResponse,
  MessageEvent,
  RealtimeEnvelope,
  SessionUiRequest,
  TurnTimingPayload,
} from "../../lib/types";
import type { MessagesStore } from "../messages/store";

export interface LiveSessionState {
  offsetsBySessionId: Record<string, number>;
  liveOffsetsBySessionId: Record<string, number>;
  bridgeOffsetsBySessionId: Record<string, number>;
  streamCursorsBySessionId: Record<string, number>;
  uiStreamCursorsBySessionId: Record<string, number>;
  requestsBySessionId: Record<string, SessionUiRequest[]>;
  requestVersionsBySessionId: Record<string, string>;
  busyBySessionId: Record<string, boolean>;
  loadingBySessionId: Record<string, boolean>;
  errorBySessionId: Record<string, string>;
  tokenBySessionId: Record<string, Record<string, unknown> | null>;
  contextUsageBySessionId: Record<string, ContextUsagePayload | null>;
  turnTimingBySessionId: Record<string, TurnTimingPayload | null>;
}

export interface LiveSessionStore {
  getState(): LiveSessionState;
  subscribe(listener: () => void): () => void;
  loadInitial(sessionId: string, runtimeId?: string | null): Promise<void>;
  poll(sessionId: string, runtimeId?: string | null): Promise<void>;
  applyFrame(frame: RealtimeEnvelope): void;
  resetSession(sessionId: string): void;
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

function nextCursor(prior: number | undefined, value: unknown) {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return prior ?? 0;
  }
  return Math.max(prior ?? 0, Math.floor(value));
}

function normalizeSnapshotEvents(payload: LiveSessionResponse) {
  if (Array.isArray(payload.events)) {
    return payload.events;
  }
  return [] as MessageEvent[];
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
    loadingBySessionId: {},
    errorBySessionId: {},
    tokenBySessionId: {},
    contextUsageBySessionId: {},
    turnTimingBySessionId: {},
  };
  const listeners = new Set<() => void>();
  const inFlightBySessionId: Record<string, Promise<void> | undefined> = {};
  const streamingTextBySessionId = new Map<string, Map<string, string>>();

  const emit = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  const applySnapshot = (sessionId: string, payload: LiveSessionResponse, replace: boolean) => {
    messagesStore.applyLive(sessionId, normalizeSnapshotEvents(payload), {
      replace,
      offset: typeof payload.offset === "number" ? payload.offset : state.offsetsBySessionId[sessionId],
      hasOlder: payload.has_older === true,
      nextBefore: typeof payload.next_before === "number" ? payload.next_before : undefined,
    });
    const nextRequests = Array.isArray(payload.requests)
      ? payload.requests.map((request) => normalizeRequest(request)).filter((request): request is SessionUiRequest => request !== null)
      : state.requestsBySessionId[sessionId] ?? [];
    const nextRequestVersionsBySessionId = { ...state.requestVersionsBySessionId };
    if (typeof payload.requests_version === "string") {
      nextRequestVersionsBySessionId[sessionId] = payload.requests_version;
    }
    state = {
      ...state,
      offsetsBySessionId: {
        ...state.offsetsBySessionId,
        [sessionId]: typeof payload.offset === "number" ? payload.offset : state.offsetsBySessionId[sessionId] ?? 0,
      },
      liveOffsetsBySessionId: {
        ...state.liveOffsetsBySessionId,
        [sessionId]: typeof payload.live_offset === "number" ? payload.live_offset : state.liveOffsetsBySessionId[sessionId] ?? 0,
      },
      bridgeOffsetsBySessionId: {
        ...state.bridgeOffsetsBySessionId,
        [sessionId]: typeof payload.bridge_offset === "number" ? payload.bridge_offset : state.bridgeOffsetsBySessionId[sessionId] ?? 0,
      },
      streamCursorsBySessionId: {
        ...state.streamCursorsBySessionId,
        [sessionId]: typeof payload.stream_cursors?.session === "number"
          ? payload.stream_cursors.session
          : typeof payload.stream_seq === "number"
            ? payload.stream_seq
            : state.streamCursorsBySessionId[sessionId] ?? 0,
      },
      uiStreamCursorsBySessionId: {
        ...state.uiStreamCursorsBySessionId,
        [sessionId]: typeof payload.stream_cursors?.ui === "number"
          ? payload.stream_cursors.ui
          : typeof payload.ui_stream_seq === "number"
            ? payload.ui_stream_seq
            : state.uiStreamCursorsBySessionId[sessionId] ?? 0,
      },
      requestsBySessionId: {
        ...state.requestsBySessionId,
        [sessionId]: nextRequests,
      },
      requestVersionsBySessionId: nextRequestVersionsBySessionId,
      busyBySessionId: {
        ...state.busyBySessionId,
        [sessionId]: payload.busy === true,
      },
      loadingBySessionId: {
        ...state.loadingBySessionId,
        [sessionId]: false,
      },
      errorBySessionId: {
        ...state.errorBySessionId,
        [sessionId]: "",
      },
      tokenBySessionId: {
        ...state.tokenBySessionId,
        [sessionId]: toObjectRecord(payload.token),
      },
      contextUsageBySessionId: {
        ...state.contextUsageBySessionId,
        [sessionId]: toObjectRecord(payload.context_usage) as ContextUsagePayload | null,
      },
      turnTimingBySessionId: {
        ...state.turnTimingBySessionId,
        [sessionId]: toObjectRecord(payload.turn_timing) as TurnTimingPayload | null,
      },
    };
    emit();
  };

  const runLoad = async (sessionId: string, replace: boolean, runtimeId?: string | null) => {
    const existing = inFlightBySessionId[sessionId];
    if (existing) {
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
        const offset = replace ? undefined : state.offsetsBySessionId[sessionId];
        const liveOffset = replace ? undefined : state.liveOffsetsBySessionId[sessionId];
        const bridgeOffset = replace ? undefined : state.bridgeOffsetsBySessionId[sessionId];
        const requestsVersion = replace ? undefined : state.requestVersionsBySessionId[sessionId];
        const payload = typeof (api as { getSessionState?: unknown }).getSessionState === "function"
          ? runtimeId
            ? await (api as { getSessionState(sessionId: string, signal?: AbortSignal, runtimeId?: string | null): Promise<LiveSessionResponse> }).getSessionState(sessionId, undefined, runtimeId)
            : await (api as { getSessionState(sessionId: string, signal?: AbortSignal, runtimeId?: string | null): Promise<LiveSessionResponse> }).getSessionState(sessionId)
          : runtimeId
            ? await (api as { getLiveSession(sessionId: string, offset?: number, requestsVersion?: string, signal?: AbortSignal, liveOffset?: number, runtimeId?: string | null, bridgeOffset?: number): Promise<LiveSessionResponse> }).getLiveSession(sessionId, offset, requestsVersion, undefined, liveOffset, runtimeId, bridgeOffset)
            : await (api as { getLiveSession(sessionId: string, offset?: number, requestsVersion?: string, signal?: AbortSignal, liveOffset?: number, runtimeId?: string | null, bridgeOffset?: number): Promise<LiveSessionResponse> }).getLiveSession(sessionId, offset, requestsVersion, undefined, liveOffset, undefined, bridgeOffset);
        applySnapshot(sessionId, payload, replace);
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
    applyFrame(frame: RealtimeEnvelope) {
      const type = String(frame.type || "").trim();
      const sessionId = resolveSessionId(frame);
      const payload = toObjectRecord(frame.payload);
      if (!type || !sessionId) {
        return;
      }

      if (type === "session.state") {
        state = {
          ...state,
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          busyBySessionId: {
            ...state.busyBySessionId,
            [sessionId]: payload?.busy === true,
          },
          errorBySessionId: {
            ...state.errorBySessionId,
            [sessionId]: "",
          },
        };
        emit();
        return;
      }

      if (type === "message.delta") {
        const turnId = typeof payload?.turn_id === "string" && payload.turn_id.trim()
          ? payload.turn_id.trim()
          : String(frame.id || `stream_${Date.now()}`);
        const perSession = streamingTextBySessionId.get(sessionId) ?? new Map<string, string>();
        const previous = perSession.get(turnId) || "";
        const delta = typeof payload?.delta === "string" ? payload.delta : "";
        const nextText = previous + delta;
        perSession.set(turnId, nextText);
        streamingTextBySessionId.set(sessionId, perSession);
        appendRealtimeEvent(sessionId, {
          event_id: typeof frame.id === "string" ? frame.id : undefined,
          role: typeof payload?.role === "string" ? payload.role : "assistant",
          streaming: true,
          completed: false,
          stream_id: turnId,
          turn_id: turnId,
          text: nextText,
          ts: typeof frame.ts === "number" ? frame.ts : undefined,
        });
        state = {
          ...state,
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
          busyBySessionId: {
            ...state.busyBySessionId,
            [sessionId]: true,
          },
        };
        emit();
        return;
      }

      if (type === "message.commit") {
        const turnId = typeof payload?.turn_id === "string" && payload.turn_id.trim()
          ? payload.turn_id.trim()
          : "";
        if (turnId) {
          streamingTextBySessionId.get(sessionId)?.delete(turnId);
        }
        const message = toObjectRecord(payload?.message);
        appendRealtimeEvent(sessionId, {
          ...message,
          event_id: typeof frame.id === "string" ? frame.id : undefined,
          turn_id: turnId || (typeof message?.turn_id === "string" ? message.turn_id : undefined),
          role: typeof message?.role === "string" ? message.role : typeof payload?.role === "string" ? payload.role : undefined,
          text: typeof message?.text === "string" ? message.text : undefined,
          ts: typeof message?.ts === "number" ? message.ts : typeof frame.ts === "number" ? frame.ts : undefined,
        } as MessageEvent);
        state = {
          ...state,
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: nextCursor(state.streamCursorsBySessionId[sessionId], payload?.stream_seq),
          },
        };
        emit();
        return;
      }

      if (type === "ui.request") {
        const request = normalizeRequest(payload?.request);
        if (!request) {
          return;
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
        return;
      }

      if (type === "ui.resolved") {
        const requestId = typeof payload?.request_id === "string" && payload.request_id.trim()
          ? payload.request_id.trim()
          : typeof payload?.response_to === "string" && payload.response_to.trim()
            ? payload.response_to.trim()
            : "";
        if (!requestId) {
          return;
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
        return;
      }

      if (type === "transport.reset_required") {
        state = {
          ...state,
          errorBySessionId: {
            ...state.errorBySessionId,
            [sessionId]: typeof payload?.reason === "string" ? payload.reason : "transport reset required",
          },
          streamCursorsBySessionId: {
            ...state.streamCursorsBySessionId,
            [sessionId]: 0,
          },
          uiStreamCursorsBySessionId: {
            ...state.uiStreamCursorsBySessionId,
            [sessionId]: 0,
          },
        };
        emit();
      }
    },
    resetSession(sessionId: string) {
      streamingTextBySessionId.delete(sessionId);
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
      };
      emit();
    },
  };
}
