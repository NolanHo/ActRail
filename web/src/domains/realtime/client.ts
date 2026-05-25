import type { RealtimeEnvelope } from "../../lib/types";
import { frameToRealtimeEnvelope, sendConnectCommand, subscribeConnectEvents, type ConnectWireFormat } from "./connect";

export type RealtimeConnectionState = "idle" | "connecting" | "open" | "error" | "closed";

export interface RealtimeClientConfig {
  protocolVersion?: number;
  connectBasePath?: string | null;
  connectWireFormat?: ConnectWireFormat;
}

export interface RealtimeStreamSubscription {
  name: string;
  resumeFrom?: number;
  suppressMessageDeltas?: boolean;
}

export interface RealtimeCommand {
  type: string;
  stream: string;
  payload: Record<string, unknown>;
}

const DEFAULT_PROTOCOL_VERSION = 1;
const DEFAULT_RECONNECT_DELAY_MS = 1000;

let config: RealtimeClientConfig = {
  protocolVersion: DEFAULT_PROTOCOL_VERSION,
  connectBasePath: "/api/connect",
  connectWireFormat: "json",
};
let state: RealtimeConnectionState = "idle";
let connectPromise: Promise<void> | null = null;
let reconnectTimer: number | null = null;
let shouldReconnect = false;
let connectAbortController: AbortController | null = null;
let connectLastEventId = 0;
let currentSubscriptions: RealtimeStreamSubscription[] = [];
let currentSubscriptionsKey = "[]";

const frameListeners = new Set<(frame: RealtimeEnvelope) => void>();
const stateListeners = new Set<(next: RealtimeConnectionState) => void>();

function emitState(next: RealtimeConnectionState) {
  state = next;
  for (const listener of stateListeners) {
    listener(next);
  }
}

function emitFrame(frame: RealtimeEnvelope) {
  for (const listener of frameListeners) {
    listener(frame);
  }
}

function clearReconnectTimer() {
  if (reconnectTimer !== null && typeof window !== "undefined") {
    window.clearTimeout(reconnectTimer);
  }
  reconnectTimer = null;
}

function scheduleReconnect() {
  if (!shouldReconnect || reconnectTimer !== null || typeof window === "undefined") {
    return;
  }
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = null;
    void connect();
  }, DEFAULT_RECONNECT_DELAY_MS);
}

function connectConfig() {
  return { basePath: config.connectBasePath || "/api/connect", wireFormat: (config.connectWireFormat || "json") as ConnectWireFormat };
}

function handleFrame(frame: RealtimeEnvelope) {
  emitFrame(frame);
}

function normalizeSubscriptions(subscriptions: RealtimeStreamSubscription[]) {
  const byName = new Map<string, RealtimeStreamSubscription>();
  for (const subscription of subscriptions) {
    const name = String(subscription.name || "").trim();
    if (!name) {
      continue;
    }
    const resumeFrom = typeof subscription.resumeFrom === "number" && Number.isFinite(subscription.resumeFrom)
      ? Math.max(0, Math.floor(subscription.resumeFrom))
      : undefined;
    const existing = byName.get(name);
    byName.set(name, {
      name,
      resumeFrom: existing?.resumeFrom !== undefined || resumeFrom !== undefined
        ? Math.max(existing?.resumeFrom ?? 0, resumeFrom ?? 0)
        : undefined,
      suppressMessageDeltas: Boolean(existing?.suppressMessageDeltas || subscription.suppressMessageDeltas),
    });
  }
  return Array.from(byName.values()).sort((a, b) => a.name.localeCompare(b.name));
}

function restartConnectStream() {
  clearReconnectTimer();
  const controller = connectAbortController;
  connectAbortController = null;
  connectPromise = null;
  if (controller) {
    controller.abort();
  }
  if (shouldReconnect) {
    void connect().catch(() => undefined);
  }
}

export function configureRealtimeClient(next: RealtimeClientConfig) {
  const nextProtocolVersion = next.protocolVersion ?? config.protocolVersion ?? DEFAULT_PROTOCOL_VERSION;
  const nextConnectBasePath = String(next.connectBasePath || "").trim() || null;
  const nextConnectWireFormat = next.connectWireFormat || "json";
  const changed = nextProtocolVersion !== config.protocolVersion
    || nextConnectBasePath !== config.connectBasePath
    || nextConnectWireFormat !== (config.connectWireFormat || "json");
  config = {
    protocolVersion: nextProtocolVersion,
    connectBasePath: nextConnectBasePath,
    connectWireFormat: nextConnectWireFormat,
  };
  if (changed && connectAbortController) {
    const reconnect = shouldReconnect;
    disconnect();
    if (reconnect) {
      void connect().catch(() => undefined);
    }
  }
}

export function getRealtimeConnectionState() {
  return state;
}

export function subscribeRealtimeFrames(listener: (frame: RealtimeEnvelope) => void) {
  frameListeners.add(listener);
  return () => {
    frameListeners.delete(listener);
  };
}

export function subscribeRealtimeState(listener: (next: RealtimeConnectionState) => void) {
  stateListeners.add(listener);
  listener(state);
  return () => {
    stateListeners.delete(listener);
  };
}

export function setRealtimeSubscriptions(subscriptions: RealtimeStreamSubscription[]) {
  const normalized = normalizeSubscriptions(subscriptions);
  const nextKey = JSON.stringify(normalized);
  if (nextKey === currentSubscriptionsKey) {
    return;
  }
  currentSubscriptions = normalized;
  currentSubscriptionsKey = nextKey;
  if (connectAbortController || connectPromise || reconnectTimer !== null) {
    restartConnectStream();
  }
}

export async function connect() {
  if (connectPromise) {
    return connectPromise;
  }
  if (typeof window === "undefined") {
    return;
  }

  shouldReconnect = true;
  emitState("connecting");
  const controller = new AbortController();
  connectAbortController = controller;
  const wrapped = subscribeConnectEvents(connectConfig(), connectLastEventId, currentSubscriptions, (rawFrame) => {
    const frame = frameToRealtimeEnvelope(rawFrame);
    if (!frame) return;
    const id = Number(frame.id || 0);
    if (Number.isFinite(id) && id > 0) {
      if (id <= connectLastEventId) return;
      connectLastEventId = id;
    }
    handleFrame(frame);
  }, controller.signal).then(() => {
    if (connectPromise === wrapped) {
      connectPromise = null;
    }
    if (connectAbortController === controller) {
      connectAbortController = null;
      emitState("closed");
      if (shouldReconnect) scheduleReconnect();
    }
  }).catch((error) => {
    if (connectPromise === wrapped) {
      connectPromise = null;
    }
    if (connectAbortController === controller) {
      connectAbortController = null;
    }
    if (!controller.signal.aborted) {
      emitState("error");
      scheduleReconnect();
      throw error;
    }
  });
  connectPromise = wrapped;
  emitState("open");
  return;
}

export function disconnect() {
  shouldReconnect = false;
  clearReconnectTimer();
  if (connectAbortController) {
    connectAbortController.abort();
    connectAbortController = null;
  }
  connectPromise = null;
  emitState("closed");
}

export async function sendRealtimeCommand(command: RealtimeCommand) {
  if (!command.type.trim()) {
    throw new Error("Realtime command type required");
  }
  if (!command.stream.trim()) {
    throw new Error("Realtime command stream required");
  }
  return sendConnectCommand(connectConfig(), command);
}
