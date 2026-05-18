import { api } from "../../lib/api";
import type { MessageEvent } from "../../lib/types";

const COMPOSER_DRAFTS_STORAGE_KEY = "actrail.composerDrafts.v1";
const DRAFT_PERSIST_DEBOUNCE_MS = 250;

export interface PendingComposerMessage {
  localId: string;
  role: "user";
  text: string;
  pending: true;
  ts: number;
  requestId?: string;
  [key: string]: unknown;
}

export interface ComposerState {
  draftBySessionId: Record<string, string>;
  sending: boolean;
  sendingBySessionId: Record<string, boolean>;
  pendingBySessionId: Record<string, PendingComposerMessage[]>;
}

export interface ComposerStore {
  getState(): ComposerState;
  subscribe(listener: () => void): () => void;
  setDraft(sessionId: string | null | undefined, value: string): void;
  copyDraft(sourceSessionId: string | null | undefined, targetSessionId: string | null | undefined): void;
  submit(sessionId: string, runtimeId?: string | null): Promise<unknown>;
  clearAcknowledgedPending(sessionId: string, persistedEvents: MessageEvent[]): void;
}

function readPersistedDrafts(): Record<string, string> {
  if (typeof window === "undefined") {
    return {} as Record<string, string>;
  }

  try {
    const raw = window.localStorage.getItem(COMPOSER_DRAFTS_STORAGE_KEY);
    if (!raw) {
      return {} as Record<string, string>;
    }

    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") {
      return {} as Record<string, string>;
    }

    const drafts: Record<string, string> = {};
    for (const [sessionId, value] of Object.entries(parsed)) {
      if (typeof sessionId === "string" && typeof value === "string" && value.length > 0) {
        drafts[sessionId] = value;
      }
    }

    return drafts;
  } catch {
    return {} as Record<string, string>;
  }
}

function parseSlashCommand(text: string) {
  const trimmed = text.trim();
  const body = trimmed.startsWith("/") ? trimmed.slice(1) : trimmed;
  const splitAt = body.search(/\s/);
  if (splitAt === -1) {
    return { name: body, args: "" };
  }
  return {
    name: body.slice(0, splitAt),
    args: body.slice(splitAt + 1).trim(),
  };
}

function persistDrafts(draftBySessionId: Record<string, string>) {
  if (typeof window === "undefined") {
    return;
  }

  try {
    if (Object.keys(draftBySessionId).length === 0) {
      window.localStorage.removeItem(COMPOSER_DRAFTS_STORAGE_KEY);
      return;
    }
    window.localStorage.setItem(COMPOSER_DRAFTS_STORAGE_KEY, JSON.stringify(draftBySessionId));
  } catch {
    // localStorage persistence is best-effort only.
  }
}

function timestampSeconds(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value > 1_000_000_000_000 ? value / 1000 : value;
  }
  if (typeof value === "string" && value.trim()) {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) {
      return parsed / 1000;
    }
  }
  return null;
}

function persistedUserAcknowledgesPending(event: MessageEvent, pending: PendingComposerMessage): boolean {
  if (event?.role !== "user" || typeof event?.text !== "string" || event.text !== pending.text) {
    return false;
  }
  const pendingTS = timestampSeconds(pending.ts);
  const eventTS = timestampSeconds(event.ts);
  if (pendingTS !== null) {
    return eventTS !== null && eventTS >= pendingTS - 2;
  }
  return true;
}

function nextDraftMap(draftBySessionId: Record<string, string>, sessionId: string | null | undefined, value: string) {
  if (!sessionId) {
    return draftBySessionId;
  }

  if (!value.length) {
    if (!(sessionId in draftBySessionId)) {
      return draftBySessionId;
    }

    const next = { ...draftBySessionId };
    delete next[sessionId];
    return next;
  }

  if (draftBySessionId[sessionId] === value) {
    return draftBySessionId;
  }

  return {
    ...draftBySessionId,
    [sessionId]: value,
  };
}

export function createComposerStore(): ComposerStore {
  let state: ComposerState = { draftBySessionId: readPersistedDrafts(), sending: false, sendingBySessionId: {}, pendingBySessionId: {} };
  const listeners = new Set<() => void>();
  let nextPendingId = 0;
  let draftPersistTimerId: number | null = null;
  let pendingPersistDrafts: Record<string, string> | null = null;

  const emit = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  const flushDraftPersistence = (draftBySessionId = state.draftBySessionId) => {
    if (draftPersistTimerId !== null && typeof window !== "undefined") {
      window.clearTimeout(draftPersistTimerId);
    }
    draftPersistTimerId = null;
    pendingPersistDrafts = null;
    persistDrafts(draftBySessionId);
  };

  const scheduleDraftPersistence = (draftBySessionId: Record<string, string>) => {
    if (typeof window === "undefined") {
      return;
    }
    pendingPersistDrafts = draftBySessionId;
    if (draftPersistTimerId !== null) {
      window.clearTimeout(draftPersistTimerId);
    }
    draftPersistTimerId = window.setTimeout(() => {
      const drafts = pendingPersistDrafts;
      draftPersistTimerId = null;
      pendingPersistDrafts = null;
      if (drafts) {
        persistDrafts(drafts);
      }
    }, DRAFT_PERSIST_DEBOUNCE_MS);
  };

  const updateDrafts = (sessionId: string | null | undefined, value: string, persistMode: "defer" | "now" = "defer") => {
    const draftBySessionId = nextDraftMap(state.draftBySessionId, sessionId, value);
    if (draftBySessionId === state.draftBySessionId) {
      return false;
    }

    state = { ...state, draftBySessionId };
    if (persistMode === "now") {
      flushDraftPersistence(draftBySessionId);
    } else {
      scheduleDraftPersistence(draftBySessionId);
    }
    return true;
  };

  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    setDraft(sessionId: string | null | undefined, value: string) {
      if (updateDrafts(sessionId, value)) {
        emit();
      }
    },
    copyDraft(sourceSessionId: string | null | undefined, targetSessionId: string | null | undefined) {
      const sourceId = typeof sourceSessionId === "string" ? sourceSessionId : "";
      const targetId = typeof targetSessionId === "string" ? targetSessionId : "";
      if (!sourceId || !targetId || sourceId === targetId) {
        return;
      }
      const draft = state.draftBySessionId[sourceId] ?? "";
      if (!draft.length) {
        return;
      }
      const draftBySessionId = nextDraftMap(state.draftBySessionId, targetId, draft);
      if (draftBySessionId === state.draftBySessionId) {
        return;
      }
      state = { ...state, draftBySessionId };
      flushDraftPersistence(draftBySessionId);
      emit();
    },
    async submit(sessionId: string, runtimeId?: string | null) {
      const text = state.draftBySessionId[sessionId] ?? "";
      const normalizedText = text.trim();
      if (!normalizedText || state.sendingBySessionId[sessionId]) return;
      const slashCommand = normalizedText.startsWith("/");

      nextPendingId += 1;
      const pendingMessage: PendingComposerMessage = {
        localId: `local-pending-${nextPendingId}`,
        role: "user",
        text,
        pending: true,
        ts: Date.now() / 1000,
        request_state: "sending",
      };

      state = {
        ...state,
        draftBySessionId: nextDraftMap(state.draftBySessionId, sessionId, ""),
        sending: true,
        sendingBySessionId: {
          ...state.sendingBySessionId,
          [sessionId]: true,
        },
        pendingBySessionId: slashCommand
          ? state.pendingBySessionId
          : {
            ...state.pendingBySessionId,
            [sessionId]: [...(state.pendingBySessionId[sessionId] ?? []), pendingMessage],
          },
      };
      flushDraftPersistence(state.draftBySessionId);
      emit();

      try {
        const response = slashCommand
          ? await api.executeSessionCommand(sessionId, parseSlashCommand(normalizedText), runtimeId)
          : runtimeId
            ? await api.sendMessage(sessionId, normalizedText, runtimeId)
            : await api.sendMessage(sessionId, normalizedText);
        const requestId = response && typeof response === "object" && typeof (response as { request_id?: unknown }).request_id === "string"
          ? String((response as { request_id?: unknown }).request_id)
          : "";
        const nextSendingBySessionId = { ...state.sendingBySessionId };
        delete nextSendingBySessionId[sessionId];
        state = {
          ...state,
          sending: Object.values(nextSendingBySessionId).some(Boolean),
          sendingBySessionId: nextSendingBySessionId,
          pendingBySessionId: slashCommand
            ? state.pendingBySessionId
            : {
              ...state.pendingBySessionId,
              [sessionId]: (state.pendingBySessionId[sessionId] ?? []).map((item) => item.localId === pendingMessage.localId
                ? { ...item, requestId: requestId || item.requestId }
                : item),
            },
        };
        emit();
        return response;
      } catch (error) {
        const nextSendingBySessionId = { ...state.sendingBySessionId };
        delete nextSendingBySessionId[sessionId];
        state = {
          ...state,
          draftBySessionId: nextDraftMap(state.draftBySessionId, sessionId, state.draftBySessionId[sessionId] ? state.draftBySessionId[sessionId] : text),
          sending: Object.values(nextSendingBySessionId).some(Boolean),
          sendingBySessionId: nextSendingBySessionId,
          pendingBySessionId: slashCommand
            ? state.pendingBySessionId
            : {
              ...state.pendingBySessionId,
              [sessionId]: (state.pendingBySessionId[sessionId] ?? []).filter((item) => item.localId !== pendingMessage.localId),
            },
        };
        flushDraftPersistence(state.draftBySessionId);
        emit();
        throw error;
      }
    },
    clearAcknowledgedPending(sessionId: string, persistedEvents: MessageEvent[]) {
      const pending = state.pendingBySessionId[sessionId] ?? [];
      if (!pending.length) return;

      const persistedUsers = persistedEvents
        .filter((event) => event?.role === "user" && typeof event?.text === "string");
      const failedRequestIds = new Set(
        persistedEvents
          .filter((event) => typeof event?.request_id === "string" && event?.request_state === "failed")
          .map((event) => String(event.request_id)),
      );
      const failedTexts = new Set(
        persistedEvents
          .filter((event) => event?.request_state === "failed" && typeof event?.pending_text === "string")
          .map((event) => String(event.pending_text)),
      );
      if (!persistedUsers.length && !failedRequestIds.size && !failedTexts.size) return;

      const acknowledgedLocalIds = new Set<string>();
      let persistedIdx = persistedUsers.length - 1;
      let pendingIdx = pending.length - 1;
      while (persistedIdx >= 0 && pendingIdx >= 0) {
        if (persistedUserAcknowledgesPending(persistedUsers[persistedIdx], pending[pendingIdx])) {
          acknowledgedLocalIds.add(pending[pendingIdx].localId);
          pendingIdx -= 1;
        }
        persistedIdx -= 1;
      }
      for (const item of pending) {
        if ((item.requestId && failedRequestIds.has(item.requestId)) || failedTexts.has(item.text)) {
          acknowledgedLocalIds.add(item.localId);
        }
      }
      if (!acknowledgedLocalIds.size) return;

      state = {
        ...state,
        pendingBySessionId: {
          ...state.pendingBySessionId,
          [sessionId]: pending.filter((item) => !acknowledgedLocalIds.has(item.localId)),
        },
      };
      emit();
    },
  };
}
