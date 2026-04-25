import { api } from "../../lib/api";
import type { MessageEvent } from "../../lib/types";

const HISTORY_PAGE_SIZE = 300;

const MACHINE_TRACE_TYPES = new Set(["reasoning", "tool", "tool_result", "todo_snapshot"]);

function normalizeMessagePage(data: Awaited<ReturnType<typeof api.listMessages>>) {
  const events = Array.isArray(data.items)
    ? data.items
    : Array.isArray(data.events)
      ? data.events
      : [];
  return {
    events,
    offset: typeof data.offset === "number" ? data.offset : typeof data.tail_seq === "number" ? data.tail_seq : undefined,
    hasOlder: data.has_older === true || data.has_more === true,
    nextBefore: typeof data.next_before === "number"
      ? data.next_before
      : typeof data.next_before_seq === "number"
        ? data.next_before_seq
        : undefined,
  };
}

export interface MessagesState {
  bySessionId: Record<string, MessageEvent[]>;
  offsetsBySessionId: Record<string, number>;
  hasOlderBySessionId: Record<string, boolean>;
  olderBeforeBySessionId: Record<string, number>;
  loadingOlderBySessionId: Record<string, boolean>;
  loadingBySessionId: Record<string, boolean>;
  loadedBySessionId: Record<string, boolean>;
  loading: boolean;
}

export interface MessagesStore {
  getState(): MessagesState;
  subscribe(listener: () => void): () => void;
  applyLive(sessionId: string, events: MessageEvent[], options: { replace: boolean; offset?: number; hasOlder?: boolean; nextBefore?: number }): void;
  applySnapshot(sessionId: string, events: MessageEvent[], options: { offset?: number; hasOlder?: boolean; nextBefore?: number }): void;
  loadInitial(sessionId: string): Promise<void>;
  poll(sessionId: string): Promise<void>;
  loadOlder(sessionId: string, limit?: number): Promise<void>;
}

function isStreamingAssistantEvent(event: MessageEvent | undefined): event is MessageEvent {
  return Boolean(
    event
    && event.role === "assistant"
    && event.streaming === true
    && typeof event.stream_id === "string"
    && event.stream_id.length > 0,
  );
}

function stableMessageKey(event: MessageEvent | undefined): string | null {
  if (!event) {
    return null;
  }
  if (typeof event.seq === "number" && Number.isFinite(event.seq)) {
    return `seq:${Math.floor(event.seq)}`;
  }
  const eventId = typeof event.event_id === "string" ? event.event_id.trim() : "";
  if (eventId) {
    return `event_id:${eventId}`;
  }
  const id = typeof event.id === "string" ? event.id.trim() : "";
  if (id) {
    return `id:${id}`;
  }
  if (isStreamingAssistantEvent(event)) {
    return `stream:${event.stream_id}`;
  }
  return null;
}

function canMergeSnapshotEvents(events: MessageEvent[]): boolean {
  return events.every((event) => stableMessageKey(event) !== null);
}

function mergeSnapshotEvents(priorEvents: MessageEvent[], snapshotEvents: MessageEvent[]): MessageEvent[] {
  if (!snapshotEvents.length) {
    return [];
  }
  if (!canMergeSnapshotEvents(snapshotEvents)) {
    return [...snapshotEvents];
  }
  return mergeLiveEvents(
    priorEvents.filter((event) => stableMessageKey(event) !== null && !isStreamingAssistantEvent(event)),
    snapshotEvents,
  );
}

function streamedAssistantMatchesDurable(streamingEvent: MessageEvent, durableEvent: MessageEvent): boolean {
  if (durableEvent.role !== "assistant" || durableEvent.streaming === true) {
    return false;
  }
  if (
    typeof streamingEvent.turn_id === "string"
    && streamingEvent.turn_id.length > 0
    && streamingEvent.turn_id === durableEvent.turn_id
  ) {
    return true;
  }
  return Boolean(
    streamingEvent.completed === true
    && typeof streamingEvent.text === "string"
    && streamingEvent.text.length > 0
    && streamingEvent.text === durableEvent.text,
  );
}

function eventCreatesConversationAnchor(event: MessageEvent | undefined): boolean {
  if (!event || event.display === false) {
    return false;
  }
  if (event.role === "user" || event.role === "assistant") {
    return true;
  }
  const type = typeof event.type === "string" ? event.type : "";
  if (!type) {
    return false;
  }
  return !MACHINE_TRACE_TYPES.has(type);
}

function mergeLiveEvents(priorEvents: MessageEvent[], incomingEvents: MessageEvent[]): MessageEvent[] {
  const next = [...priorEvents];

  for (const incomingEvent of incomingEvents) {
    const incomingKey = stableMessageKey(incomingEvent);
    if (incomingKey) {
      const existingIndex = next.findIndex((event) => stableMessageKey(event) === incomingKey);
      if (existingIndex >= 0) {
        next[existingIndex] = { ...next[existingIndex], ...incomingEvent };
      } else {
        next.push(incomingEvent);
      }
      continue;
    }

    const filtered = next.filter((event) => !isStreamingAssistantEvent(event) || !streamedAssistantMatchesDurable(event, incomingEvent));
    filtered.push(incomingEvent);
    next.splice(0, next.length, ...filtered);
  }

  return next;
}

export function createMessagesStore(): MessagesStore {
  let state: MessagesState = {
    bySessionId: {},
    offsetsBySessionId: {},
    hasOlderBySessionId: {},
    olderBeforeBySessionId: {},
    loadingOlderBySessionId: {},
    loadingBySessionId: {},
    loadedBySessionId: {},
    loading: false,
  };
  const listeners = new Set<() => void>();
  const currentLoadIds: Record<string, number> = {};
  const currentOlderLoadIds: Record<string, number> = {};
  const inFlightLoads: Record<string, Promise<void> | undefined> = {};
  const inFlightLoadTokens: Record<string, object | undefined> = {};

  const emit = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  const recomputeLoading = (loadingBySessionId: Record<string, boolean>) =>
    Object.values(loadingBySessionId).some((value) => value === true);

  const load = async (sessionId: string, init: boolean) => {
    if (!init) {
      const existing = inFlightLoads[sessionId];
      if (existing) {
        return existing;
      }
    }

    const loadId = (currentLoadIds[sessionId] || 0) + 1;
    currentLoadIds[sessionId] = loadId;
    const requestToken = {};
    inFlightLoadTokens[sessionId] = requestToken;
    const request = (async () => {
      state = {
        ...state,
        loadingBySessionId: {
          ...state.loadingBySessionId,
          [sessionId]: true,
        },
        loading: true,
      };
      emit();

      try {
        const data = normalizeMessagePage(await api.listMessages(sessionId, init));
        if (loadId !== currentLoadIds[sessionId]) {
          return;
        }
        const priorEvents = init ? [] : state.bySessionId[sessionId] ?? [];
        const nextLoadingBySessionId = {
          ...state.loadingBySessionId,
          [sessionId]: false,
        };
        state = {
          bySessionId: {
            ...state.bySessionId,
            [sessionId]: mergeSnapshotEvents(priorEvents, data.events),
          },
          offsetsBySessionId: {
            ...state.offsetsBySessionId,
            [sessionId]: typeof data.offset === "number" ? data.offset : state.offsetsBySessionId[sessionId] ?? 0,
          },
          hasOlderBySessionId: {
            ...state.hasOlderBySessionId,
            [sessionId]: init ? data.hasOlder === true : state.hasOlderBySessionId[sessionId] ?? false,
          },
          olderBeforeBySessionId: {
            ...state.olderBeforeBySessionId,
            [sessionId]: init
              ? typeof data.nextBefore === "number"
                ? data.nextBefore
                : 0
              : state.olderBeforeBySessionId[sessionId] ?? 0,
          },
          loadingOlderBySessionId: {
            ...state.loadingOlderBySessionId,
            [sessionId]: false,
          },
          loadingBySessionId: nextLoadingBySessionId,
          loadedBySessionId: {
            ...state.loadedBySessionId,
            [sessionId]: true,
          },
          loading: recomputeLoading(nextLoadingBySessionId),
        };
        emit();
      } catch (error) {
        if (loadId === currentLoadIds[sessionId]) {
          const nextLoadingBySessionId = {
            ...state.loadingBySessionId,
            [sessionId]: false,
          };
          state = {
            ...state,
            loadingBySessionId: nextLoadingBySessionId,
            loading: recomputeLoading(nextLoadingBySessionId),
          };
          emit();
          throw error;
        }
      } finally {
        if (inFlightLoadTokens[sessionId] === requestToken) {
          delete inFlightLoadTokens[sessionId];
          delete inFlightLoads[sessionId];
        }
      }
    })();
    inFlightLoads[sessionId] = request;
    return request;
  };

  const loadOlder = async (sessionId: string, limit = HISTORY_PAGE_SIZE) => {
    const before = state.olderBeforeBySessionId[sessionId] ?? 0;
    if (before <= 0 && !state.hasOlderBySessionId[sessionId]) {
      return;
    }

    const loadId = (currentOlderLoadIds[sessionId] || 0) + 1;
    currentOlderLoadIds[sessionId] = loadId;
    state = {
      ...state,
      loadingOlderBySessionId: {
        ...state.loadingOlderBySessionId,
        [sessionId]: true,
      },
    };
    emit();

    try {
      let nextBefore = before;
      let hasOlder = state.hasOlderBySessionId[sessionId] === true;
      let nextOffset = state.offsetsBySessionId[sessionId] ?? 0;
      let aggregatedEvents: MessageEvent[] = [];
      let foundAnchor = false;

      while ((nextBefore > 0 || hasOlder) && !foundAnchor) {
        const data = normalizeMessagePage(await api.listMessages(sessionId, true, undefined, undefined, nextBefore, limit));
        if (loadId !== currentOlderLoadIds[sessionId]) {
          return;
        }

        const pageEvents = Array.isArray(data.events) ? data.events : [];
        aggregatedEvents = [...pageEvents, ...aggregatedEvents];
        foundAnchor = pageEvents.some(eventCreatesConversationAnchor);
        hasOlder = data.hasOlder === true;
        nextBefore = typeof data.nextBefore === "number" ? data.nextBefore : 0;
        if (typeof data.offset === "number") {
          nextOffset = data.offset;
        }
        if (!pageEvents.length && !hasOlder) {
          break;
        }
      }

      state = {
        ...state,
        bySessionId: {
          ...state.bySessionId,
          [sessionId]: [...aggregatedEvents, ...(state.bySessionId[sessionId] ?? [])],
        },
        offsetsBySessionId: {
          ...state.offsetsBySessionId,
          [sessionId]: nextOffset,
        },
        hasOlderBySessionId: {
          ...state.hasOlderBySessionId,
          [sessionId]: hasOlder,
        },
        olderBeforeBySessionId: {
          ...state.olderBeforeBySessionId,
          [sessionId]: nextBefore,
        },
        loadingOlderBySessionId: {
          ...state.loadingOlderBySessionId,
          [sessionId]: false,
        },
      };
      emit();
    } catch (error) {
      if (loadId === currentOlderLoadIds[sessionId]) {
        state = {
          ...state,
          loadingOlderBySessionId: {
            ...state.loadingOlderBySessionId,
            [sessionId]: false,
          },
        };
        emit();
        throw error;
      }
    }
  };

  const applySnapshot = (sessionId: string, events: MessageEvent[], options: { offset?: number; hasOlder?: boolean; nextBefore?: number }) => {
    const priorEvents = state.bySessionId[sessionId] ?? [];
    const nextLoadingBySessionId = {
      ...state.loadingBySessionId,
      [sessionId]: false,
    };
    state = {
      ...state,
      bySessionId: {
        ...state.bySessionId,
        [sessionId]: mergeSnapshotEvents(priorEvents, events),
      },
      offsetsBySessionId: {
        ...state.offsetsBySessionId,
        [sessionId]: typeof options.offset === "number" ? options.offset : state.offsetsBySessionId[sessionId] ?? 0,
      },
      hasOlderBySessionId: {
        ...state.hasOlderBySessionId,
        [sessionId]: typeof options.hasOlder === "boolean" ? options.hasOlder : state.hasOlderBySessionId[sessionId] ?? false,
      },
      olderBeforeBySessionId: {
        ...state.olderBeforeBySessionId,
        [sessionId]: typeof options.nextBefore === "number" ? options.nextBefore : state.olderBeforeBySessionId[sessionId] ?? 0,
      },
      loadingBySessionId: nextLoadingBySessionId,
      loadedBySessionId: {
        ...state.loadedBySessionId,
        [sessionId]: true,
      },
      loading: recomputeLoading(nextLoadingBySessionId),
    };
    emit();
  };

  const applyLive = (sessionId: string, events: MessageEvent[], options: { replace: boolean; offset?: number; hasOlder?: boolean; nextBefore?: number }) => {
    const priorEvents = state.bySessionId[sessionId] ?? [];
    const nextLoadingBySessionId = {
      ...state.loadingBySessionId,
      [sessionId]: false,
    };
    const nextEvents = options.replace ? mergeLiveEvents([], events) : mergeLiveEvents(priorEvents, events);
    state = {
      ...state,
      bySessionId: {
        ...state.bySessionId,
        [sessionId]: nextEvents,
      },
      offsetsBySessionId: {
        ...state.offsetsBySessionId,
        [sessionId]: typeof options.offset === "number" ? options.offset : state.offsetsBySessionId[sessionId] ?? 0,
      },
      hasOlderBySessionId: {
        ...state.hasOlderBySessionId,
        [sessionId]: typeof options.hasOlder === "boolean" ? options.hasOlder : state.hasOlderBySessionId[sessionId] ?? false,
      },
      olderBeforeBySessionId: {
        ...state.olderBeforeBySessionId,
        [sessionId]: typeof options.nextBefore === "number" ? options.nextBefore : state.olderBeforeBySessionId[sessionId] ?? 0,
      },
      loadingBySessionId: nextLoadingBySessionId,
      loadedBySessionId: {
        ...state.loadedBySessionId,
        [sessionId]: true,
      },
      loading: recomputeLoading(nextLoadingBySessionId),
    };
    emit();
  };

  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    loadInitial(sessionId: string) {
      return load(sessionId, true);
    },
    poll(sessionId: string) {
      return load(sessionId, false);
    },
    applyLive,
    applySnapshot,
    loadOlder(sessionId: string, limit?: number) {
      return loadOlder(sessionId, limit);
    },
  };
}
