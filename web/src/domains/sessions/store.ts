import { configureRealtimeClient } from "../realtime/client";
import type { ConnectWireFormat } from "../realtime/connect";
import { api } from "../../lib/api";
import type { BootstrapCapabilities, CwdGroupMeta, NewSessionDefaults, RealtimeEnvelope, SessionBootstrapResponse, SessionSummary, SessionsResponse } from "../../lib/types";

export interface UpsertSessionOptions {
  prepend?: boolean;
  select?: boolean;
}

export interface RealtimeTransportStatus {
  active: "ws" | "connect";
  connectAvailable: boolean;
  connectOptIn: boolean;
  connectEligible: boolean;
  connectPath: string;
  wireFormat: ConnectWireFormat;
}

export interface SessionsState {
  items: SessionSummary[];
  activeSessionId: string | null;
  loading: boolean;
  bootstrapLoaded: boolean;
  bootstrapCapabilities: BootstrapCapabilities | null;
  deferredFeatures: string[];
  remainingCount: number;
  newSessionDefaults: NewSessionDefaults | null;
  recentCwds: string[];
  cwdGroups: Record<string, CwdGroupMeta>;
  tmuxAvailable: boolean;
  realtimeTransport: RealtimeTransportStatus;
}

export interface RefreshSessionsOptions {
  preferNewest?: boolean;
}

export interface RefreshBootstrapOptions {
  refreshPiModels?: boolean;
}

export interface SessionsStore {
  getState(): SessionsState;
  subscribe(listener: () => void): () => void;
  refresh(options?: RefreshSessionsOptions): Promise<void>;
  refreshBootstrap(options?: RefreshBootstrapOptions): Promise<void>;
  loadMore(limit?: number): Promise<void>;
  select(sessionId: string | null): void;
  upsertSession(session: SessionSummary, options?: UpsertSessionOptions): void;
  applySessionStateFrame(frame: RealtimeEnvelope): void;
}

const PAGE_SIZE = 50;
const NEW_SESSION_DEFAULTS_CACHE_KEY = "actrail.newSessionDefaults.v1";
const SESSION_READ_CACHE_KEY = "actrail.sessionReadAssistantTs.v1";
export const EXP_TRANSPORT_KEY = "actrail.expTransport";
export const EXP_CONNECT_WIRE_FORMAT_KEY = "actrail.expConnectWireFormat";
const DEFAULT_REALTIME_TRANSPORT: RealtimeTransportStatus = {
  active: "ws",
  connectAvailable: false,
  connectOptIn: false,
  connectEligible: false,
  connectPath: "/api/connect",
  wireFormat: "json",
};

function readConnectOptIn() {
  if (typeof window === "undefined") return false;
  try {
    return window.localStorage.getItem(EXP_TRANSPORT_KEY) === "connect";
  } catch {
    return false;
  }
}

export function setConnectTransportOptIn(enabled: boolean) {
  if (typeof window === "undefined") return;
  try {
    if (enabled) {
      window.localStorage.setItem(EXP_TRANSPORT_KEY, "connect");
    } else {
      window.localStorage.removeItem(EXP_TRANSPORT_KEY);
    }
  } catch {
    // Browser storage policy blocked the opt-in; bootstrap will keep WebSocket.
  }
}

function readConnectWireFormat(): ConnectWireFormat {
  if (typeof window === "undefined") return "json";
  try {
    return window.localStorage.getItem(EXP_CONNECT_WIRE_FORMAT_KEY) === "proto" ? "proto" : "json";
  } catch {
    return "json";
  }
}

export function setConnectWireFormat(value: ConnectWireFormat) {
  if (typeof window === "undefined") return;
  try {
    if (value === "proto") {
      window.localStorage.setItem(EXP_CONNECT_WIRE_FORMAT_KEY, "proto");
    } else {
      window.localStorage.removeItem(EXP_CONNECT_WIRE_FORMAT_KEY);
    }
  } catch {
    // Browser storage policy blocked the setting; bootstrap will use JSON.
  }
}

function getRealtimeTransportStatus(data: SessionBootstrapResponse): RealtimeTransportStatus {
  const connectAvailable = data.capabilities?.exp_connect_transport === true;
  const connectEligible = connectAvailable;
  const connectOptIn = readConnectOptIn();
  const connectPath = String(data.transport?.connect_path || DEFAULT_REALTIME_TRANSPORT.connectPath).trim() || DEFAULT_REALTIME_TRANSPORT.connectPath;
  const wireFormat = readConnectWireFormat();
  return {
    active: connectEligible && connectOptIn ? "connect" : "ws",
    connectAvailable,
    connectOptIn,
    connectEligible,
    connectPath,
    wireFormat,
  };
}

function normalizeSessionsResponse(data: SessionsResponse) {
  return Array.isArray(data.items)
    ? data.items
    : Array.isArray(data.sessions)
      ? data.sessions
      : [];
}

function readCachedNewSessionDefaults(): NewSessionDefaults | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(NEW_SESSION_DEFAULTS_CACHE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as NewSessionDefaults;
    return parsed && typeof parsed === "object" ? parsed : null;
  } catch {
    return null;
  }
}

function writeCachedNewSessionDefaults(defaults: NewSessionDefaults | null) {
  if (!defaults || typeof window === "undefined") return;
  try {
    window.localStorage.setItem(NEW_SESSION_DEFAULTS_CACHE_KEY, JSON.stringify(defaults));
  } catch {
    // localStorage can be disabled; runtime defaults remain authoritative.
  }
}

function mergeStringArrays(...values: Array<string[] | undefined>) {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const list of values) {
    for (const value of list ?? []) {
      const trimmed = String(value || "").trim();
      if (!trimmed || seen.has(trimmed)) continue;
      seen.add(trimmed);
      out.push(trimmed);
    }
  }
  return out;
}

function mergeProviderModels(...values: Array<Record<string, string[]> | undefined>) {
  const out: Record<string, string[]> = {};
  for (const providerModels of values) {
    for (const [provider, models] of Object.entries(providerModels ?? {})) {
      const trimmedProvider = provider.trim();
      if (!trimmedProvider) continue;
      out[trimmedProvider] = mergeStringArrays(out[trimmedProvider], models);
    }
  }
  return Object.keys(out).length ? out : undefined;
}

function mergeNewSessionDefaults(incoming: NewSessionDefaults | null, cached: NewSessionDefaults | null): NewSessionDefaults | null {
  if (!incoming) return cached;
  if (!cached) return incoming;
  const backendNames = mergeStringArrays(Object.keys(incoming.backends ?? {}), Object.keys(cached.backends ?? {}));
  const backends = Object.fromEntries(backendNames.map((backend) => {
    const next = incoming.backends?.[backend] ?? {};
    const prior = cached.backends?.[backend] ?? {};
    return [backend, {
      ...prior,
      ...next,
      provider_choices: mergeStringArrays(next.provider_choices, prior.provider_choices),
      model_providers: mergeStringArrays(next.model_providers, prior.model_providers),
      models: mergeStringArrays(next.models, prior.models),
      provider_models: mergeProviderModels(next.provider_models, prior.provider_models),
      model: Object.prototype.hasOwnProperty.call(next, "model") ? next.model : prior.model,
      provider_choice: Object.prototype.hasOwnProperty.call(next, "provider_choice") ? next.provider_choice : prior.provider_choice,
      model_provider: Object.prototype.hasOwnProperty.call(next, "model_provider") ? next.model_provider : prior.model_provider,
    }];
  }));
  return {
    default_backend: incoming.default_backend || cached.default_backend,
    backends,
    backend_capabilities: {
      ...(cached.backend_capabilities ?? {}),
      ...(incoming.backend_capabilities ?? {}),
    },
    pi_agent_grpc_default: incoming.pi_agent_grpc_default ?? cached.pi_agent_grpc_default,
  };
}

function normalizeNewSessionDefaults(data: SessionBootstrapResponse): NewSessionDefaults | null {
  const cached = readCachedNewSessionDefaults();
  if (data.new_session_defaults) {
    return mergeNewSessionDefaults(data.new_session_defaults, cached);
  }
  const defaultBackend = String(data.launch_defaults?.default_backend || "").trim();
  const availableBackends = Array.isArray(data.launch_defaults?.available_backends)
    ? data.launch_defaults?.available_backends.filter((backend): backend is string => typeof backend === "string" && backend.trim().length > 0)
    : [];
  if (!defaultBackend && availableBackends.length === 0) {
    return null;
  }
  const backends = Object.fromEntries((availableBackends.length ? availableBackends : [defaultBackend || "codex"])
    .map((backend) => [backend, {
      provider_choices: Array.isArray(data.launch_defaults?.providers) ? data.launch_defaults.providers : [],
      models: Array.isArray(data.launch_defaults?.models) ? data.launch_defaults.models : [],
    }]));
  return mergeNewSessionDefaults({
    default_backend: defaultBackend || availableBackends[0] || "codex",
    backends,
  }, cached);
}

function sessionDedupeKey(session: SessionSummary) {
  const threadId = String(session.thread_id || "").trim();
  if (threadId && !session.historical) {
    const backend = String(session.agent_backend || "codex").trim() || "codex";
    return `thread:${backend}:${threadId}`;
  }
  return `session:${String(session.session_id || "").trim()}`;
}

function dedupeSessions(items: SessionSummary[]) {
  const representativeByKey = new Map<string, SessionSummary>();
  const representativeBySessionId = new Map<string, string>();
  const unique: SessionSummary[] = [];
  for (const session of items) {
    const sessionId = String(session.session_id || "").trim();
    if (!sessionId) {
      continue;
    }
    const key = sessionDedupeKey(session);
    const representative = representativeByKey.get(key);
    if (representative) {
      representativeBySessionId.set(sessionId, representative.session_id);
      continue;
    }
    representativeByKey.set(key, session);
    representativeBySessionId.set(sessionId, session.session_id);
    unique.push(session);
  }
  return { sessions: unique, representativeBySessionId };
}

function readSessionReadMap(): Record<string, number> {
  if (typeof window === "undefined") return {};
  try {
    const parsed = JSON.parse(window.localStorage.getItem(SESSION_READ_CACHE_KEY) || "{}");
    if (!parsed || typeof parsed !== "object") return {};
    return Object.fromEntries(Object.entries(parsed).flatMap(([key, value]) => {
      const ts = Number(value);
      return key && Number.isFinite(ts) ? [[key, ts]] : [];
    }));
  } catch {
    return {};
  }
}

function writeSessionReadMap(values: Record<string, number>) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(SESSION_READ_CACHE_KEY, JSON.stringify(values));
  } catch {
  }
}

function withAssistantUnreadState(items: SessionSummary[], activeSessionId: string | null) {
  const readMap = readSessionReadMap();
  let changed = false;
  const next = items.map((session) => {
    const sessionId = String(session.session_id || "").trim();
    const lastAssistantTS = Number(session.last_assistant_message_ts || 0);
    if (!sessionId || !Number.isFinite(lastAssistantTS) || lastAssistantTS <= 0) {
      if (session.has_unread_assistant === undefined) return session;
      const { has_unread_assistant: _unused, ...rest } = session;
      return rest;
    }
    if (readMap[sessionId] == null || sessionId === activeSessionId) {
      if (readMap[sessionId] !== lastAssistantTS) {
        readMap[sessionId] = lastAssistantTS;
        changed = true;
      }
    }
    const readTS = Number(readMap[sessionId] || 0);
    const unread = sessionId !== activeSessionId && session.busy !== true && lastAssistantTS > readTS;
    if (unread) {
      return { ...session, has_unread_assistant: true };
    }
    if (session.has_unread_assistant === undefined) return session;
    const { has_unread_assistant: _unused, ...rest } = session;
    return rest;
  });
  if (changed) {
    writeSessionReadMap(readMap);
  }
  return next;
}

function upsertSessionList(items: SessionSummary[], session: SessionSummary, prepend: boolean) {
  const sessionId = String(session.session_id || "").trim();
  if (!sessionId) {
    return items;
  }
  const existing = items.find((item) => item.session_id === sessionId) ?? null;
  const merged = { ...(existing ?? {}), ...session, session_id: sessionId };
  const withoutExisting = items.filter((item) => item.session_id !== sessionId);
  const ordered = prepend ? [merged, ...withoutExisting] : [...withoutExisting, merged];
  return dedupeSessions(ordered).sessions;
}

function sessionFingerprint(session: SessionSummary) {
  try {
    return JSON.stringify(session);
  } catch {
    return "";
  }
}

function reuseStableSessionReferences(nextItems: SessionSummary[], previousItems: SessionSummary[]) {
  if (!previousItems.length) {
    return nextItems;
  }

  const previousBySessionId = new Map(previousItems.map((session) => [session.session_id, session]));
  let changed = nextItems.length !== previousItems.length;
  const nextWithStableReferences = nextItems.map((session, index) => {
    const previous = previousBySessionId.get(session.session_id);
    if (previous && sessionFingerprint(previous) === sessionFingerprint(session)) {
      if (previousItems[index] !== previous) {
        changed = true;
      }
      return previous;
    }
    changed = true;
    return session;
  });

  return changed ? nextWithStableReferences : previousItems;
}

function optionalString(value: unknown) {
  if (typeof value !== "string") {
    return undefined;
  }
  return value;
}

function optionalNumber(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function optionalBoolean(value: unknown) {
  return typeof value === "boolean" ? value : undefined;
}

function sessionPatchFromStatePayload(payload: Record<string, unknown>): SessionSummary | null {
  const sessionId = optionalString(payload.session_id)?.trim();
  if (!sessionId) {
    return null;
  }
  const transport = payload.transport && typeof payload.transport === "object"
    ? payload.transport as Record<string, unknown>
    : null;
  const generationId = optionalString(transport?.generation_id);

  return {
    session_id: sessionId,
    ...(optionalBoolean(payload.busy) !== undefined ? { busy: optionalBoolean(payload.busy) } : {}),
    ...(optionalString(payload.busy_reason) !== undefined ? { busy_reason: optionalString(payload.busy_reason) } : {}),
    ...(optionalString(payload.runtime_state) !== undefined ? { runtime_state: optionalString(payload.runtime_state) } : {}),
    ...(optionalString(payload.runtime_state_reason) !== undefined ? { runtime_state_reason: optionalString(payload.runtime_state_reason) } : {}),
    ...(optionalNumber(payload.queue_len) !== undefined ? { queue_len: optionalNumber(payload.queue_len) } : {}),
    ...(optionalString(payload.transport_state) !== undefined ? { transport_state: optionalString(payload.transport_state) } : {}),
    ...(optionalBoolean(payload.reset_required) !== undefined ? { reset_required: optionalBoolean(payload.reset_required) } : {}),
    ...(optionalString(payload.transport_reason) !== undefined ? { transport_reason: optionalString(payload.transport_reason) } : {}),
    ...(optionalBoolean(payload.pending_startup) !== undefined ? { pending_startup: optionalBoolean(payload.pending_startup) } : {}),
    ...(generationId !== undefined ? { generation_id: generationId } : {}),
  };
}

export function createSessionsStore(): SessionsStore {
  let state: SessionsState = {
    items: [],
    activeSessionId: null,
    loading: false,
    bootstrapLoaded: false,
    bootstrapCapabilities: null,
    deferredFeatures: [],
    remainingCount: 0,
    newSessionDefaults: null,
    recentCwds: [],
    cwdGroups: {},
    tmuxAvailable: false,
    realtimeTransport: DEFAULT_REALTIME_TRANSPORT,
  };
  const listeners = new Set<() => void>();
  let currentRefreshId = 0;
  let currentBootstrapRefreshId = 0;
  let hasResolvedInitialSelection = false;
  let loadedLimit = PAGE_SIZE;
  let inFlightRefresh: { key: string; promise: Promise<void> } | null = null;

  const emit = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  const refresh = async (options?: RefreshSessionsOptions) => {
    const refreshKey = `${loadedLimit}:${options?.preferNewest === true ? "newest" : "preserve"}`;
    if (inFlightRefresh && inFlightRefresh.key === refreshKey) {
      return inFlightRefresh.promise;
    }

    const refreshId = ++currentRefreshId;
    state = { ...state, loading: true };
    emit();

    let request: Promise<void> | null = null;
    request = (async () => {
      try {
        const data = await api.listSessions({ limit: loadedLimit });
        if (refreshId !== currentRefreshId) {
          return;
        }
        const deduped = dedupeSessions(normalizeSessionsResponse(data));
        const sessions = deduped.sessions;
        const representativeBySessionId = deduped.representativeBySessionId;
        const sessionIds = new Set(sessions.map((session) => session.session_id));
        const activeRepresentativeSessionId = state.activeSessionId
          ? representativeBySessionId.get(state.activeSessionId) ?? state.activeSessionId
          : null;
        const preservedActiveSessionId = activeRepresentativeSessionId && sessionIds.has(activeRepresentativeSessionId)
          ? activeRepresentativeSessionId
          : null;
        const nextActiveSessionId = options?.preferNewest
          ? sessions[0]?.session_id ?? null
          : preservedActiveSessionId
            ?? (!hasResolvedInitialSelection ? sessions[0]?.session_id ?? null : null);
        if (nextActiveSessionId) {
          hasResolvedInitialSelection = true;
        }
        const nextItems = withAssistantUnreadState(sessions, nextActiveSessionId);
        state = {
          ...state,
          items: reuseStableSessionReferences(nextItems, state.items),
          activeSessionId: nextActiveSessionId,
          loading: false,
          remainingCount: Math.max(0, Number(data.remaining_count || 0)),
        };
        emit();
      } catch (error) {
        if (refreshId === currentRefreshId) {
          state = { ...state, loading: false };
          emit();
          throw error;
        }
      } finally {
        if (inFlightRefresh?.promise === request) {
          inFlightRefresh = null;
        }
      }
    })();

    inFlightRefresh = { key: refreshKey, promise: request };
    return request;
  };

  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    refresh,
    async refreshBootstrap(options?: RefreshBootstrapOptions) {
      const refreshId = ++currentBootstrapRefreshId;
      const data = typeof (api as { getBootstrap?: unknown }).getBootstrap === "function"
        ? await (api as { getBootstrap(options?: RefreshBootstrapOptions): Promise<SessionBootstrapResponse> }).getBootstrap(options)
        : await (api as { getSessionsBootstrap(options?: RefreshBootstrapOptions): Promise<SessionBootstrapResponse> }).getSessionsBootstrap(options);
      if (refreshId !== currentBootstrapRefreshId) {
        return;
      }
      const realtimeTransport = getRealtimeTransportStatus(data);
      configureRealtimeClient({
        protocolVersion: data.protocol_version,
        url: data.capabilities?.ws_realtime === false ? null : data.ws?.url,
        heartbeatIntervalMs: data.ws?.heartbeat_interval_ms,
        transport: realtimeTransport.active,
        connectBasePath: realtimeTransport.connectPath,
        connectWireFormat: realtimeTransport.wireFormat,
      });
      const newSessionDefaults = normalizeNewSessionDefaults(data) ?? state.newSessionDefaults;
      writeCachedNewSessionDefaults(newSessionDefaults);
      state = {
        ...state,
        bootstrapLoaded: true,
        bootstrapCapabilities: data.capabilities ?? state.bootstrapCapabilities,
        deferredFeatures: Array.isArray(data.ui?.deferred_features)
          ? data.ui.deferred_features.filter((feature): feature is string => typeof feature === "string" && feature.trim().length > 0)
          : state.deferredFeatures,
        newSessionDefaults,
        recentCwds: Array.isArray(data.recent_cwds)
          ? data.recent_cwds.filter((cwd): cwd is string => typeof cwd === "string")
          : state.recentCwds,
        cwdGroups: data.cwd_groups ?? state.cwdGroups,
        tmuxAvailable: typeof data.tmux_available === "boolean" ? data.tmux_available : state.tmuxAvailable,
        realtimeTransport,
      };
      emit();
    },
    async loadMore(limit = PAGE_SIZE) {
      if (state.remainingCount <= 0) {
        return;
      }
      loadedLimit = Math.max(PAGE_SIZE, loadedLimit + Math.max(1, limit));
      await refresh();
    },
    select(sessionId: string | null) {
      hasResolvedInitialSelection = sessionId !== null;
      state = { ...state, activeSessionId: sessionId, items: withAssistantUnreadState(state.items, sessionId) };
      emit();
    },
    upsertSession(session: SessionSummary, options?: UpsertSessionOptions) {
      const sessionId = String(session.session_id || "").trim();
      if (!sessionId) {
        return;
      }
      const nextActiveSessionId = options?.select ? sessionId : state.activeSessionId;
      hasResolvedInitialSelection = nextActiveSessionId !== null;
      state = {
        ...state,
        items: withAssistantUnreadState(upsertSessionList(state.items, session, options?.prepend !== false), nextActiveSessionId),
        activeSessionId: nextActiveSessionId,
      };
      emit();
    },
    applySessionStateFrame(frame: RealtimeEnvelope) {
      if (frame.type !== "session.state") {
        return;
      }
      const payload = frame.payload && typeof frame.payload === "object" ? frame.payload : null;
      if (!payload) {
        return;
      }
      const patch = sessionPatchFromStatePayload(payload);
      if (!patch) {
        return;
      }
      const existing = state.items.find((session) => session.session_id === patch.session_id);
      if (!existing) {
        return;
      }
      const merged = { ...existing, ...patch };
      if (sessionFingerprint(existing) === sessionFingerprint(merged)) {
        return;
      }
      const nextItems = state.items.map((session) => session.session_id === patch.session_id ? merged : session);
      state = {
        ...state,
        items: withAssistantUnreadState(reuseStableSessionReferences(nextItems, state.items), state.activeSessionId),
      };
      emit();
    },
  };
}
