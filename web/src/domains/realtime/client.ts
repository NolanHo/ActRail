import { HttpError } from "../../lib/http";
import type { RealtimeEnvelope } from "../../lib/types";

export type RealtimeConnectionState = "idle" | "connecting" | "open" | "error" | "closed";

export interface RealtimeClientConfig {
  protocolVersion?: number;
  url?: string | null;
  heartbeatIntervalMs?: number;
}

export interface RealtimeStreamSubscription {
  name: string;
  resumeFrom?: number;
}

export interface RealtimeCommand {
  type: string;
  stream: string;
  payload: Record<string, unknown>;
}

interface PendingCommand {
  reject(error: unknown): void;
  resolve(payload: Record<string, unknown>): void;
}

const DEFAULT_PROTOCOL_VERSION = 1;
const DEFAULT_COMMAND_TIMEOUT_MS = 15000;
const DEFAULT_RECONNECT_DELAY_MS = 1000;
const DEFAULT_HEARTBEAT_INTERVAL_MS = 15000;

let config: RealtimeClientConfig = {
  protocolVersion: DEFAULT_PROTOCOL_VERSION,
  url: null,
  heartbeatIntervalMs: DEFAULT_HEARTBEAT_INTERVAL_MS,
};
let state: RealtimeConnectionState = "idle";
let socket: WebSocket | null = null;
let connectPromise: Promise<void> | null = null;
let reconnectTimer: number | null = null;
let heartbeatTimer: number | null = null;
let shouldReconnect = false;
let nextRequestId = 0;

const frameListeners = new Set<(frame: RealtimeEnvelope) => void>();
const stateListeners = new Set<(next: RealtimeConnectionState) => void>();
const desiredSubscriptions = new Map<string, RealtimeStreamSubscription>();
const pendingCommands = new Map<string, PendingCommand>();

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

function clearHeartbeatTimer() {
  if (heartbeatTimer !== null && typeof window !== "undefined") {
    window.clearInterval(heartbeatTimer);
  }
  heartbeatTimer = null;
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

function resolveSocketUrl(rawUrl?: string | null) {
  const trimmed = String(rawUrl || "").trim();
  if (!trimmed) {
    return "";
  }
  if (typeof window === "undefined") {
    return trimmed;
  }
  const url = new URL(trimmed, window.location.origin);
  if (url.protocol === "http:") {
    url.protocol = "ws:";
  }
  if (url.protocol === "https:") {
    url.protocol = "wss:";
  }
  return url.toString();
}

function sendRaw(frame: Record<string, unknown>) {
  if (!socket || socket.readyState !== WebSocket.OPEN) {
    throw new Error("Realtime socket not connected");
  }
  socket.send(JSON.stringify(frame));
}

function syncSubscriptions() {
  if (!socket || socket.readyState !== WebSocket.OPEN || desiredSubscriptions.size === 0) {
    return;
  }
  sendRaw({
    type: "subscribe",
    request_id: `req_subscribe_${Date.now()}`,
    ts: Date.now() / 1000,
    stream: "system",
    payload: {
      streams: Array.from(desiredSubscriptions.values()).map((subscription) => (
        typeof subscription.resumeFrom === "number" && Number.isFinite(subscription.resumeFrom)
          ? { name: subscription.name, resume_from: Math.max(0, Math.floor(subscription.resumeFrom)) }
          : { name: subscription.name }
      )),
    },
  });
}

function startHeartbeatLoop() {
  clearHeartbeatTimer();
  if (typeof window === "undefined") {
    return;
  }
  const intervalMs = Math.max(5000, Number(config.heartbeatIntervalMs || DEFAULT_HEARTBEAT_INTERVAL_MS));
  heartbeatTimer = window.setInterval(() => {
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }
    sendRaw({
      type: "ping",
      request_id: `req_ping_${Date.now()}`,
      ts: Date.now() / 1000,
      stream: "system",
      payload: {},
    });
  }, intervalMs);
}

function rejectPendingCommands(error: unknown) {
  for (const pending of pendingCommands.values()) {
    pending.reject(error);
  }
  pendingCommands.clear();
}

function handleFrame(frame: RealtimeEnvelope) {
  const type = String(frame.type || "").trim();
  const payload = frame.payload && typeof frame.payload === "object" ? frame.payload : {};
  const requestId = typeof payload.request_id === "string"
    ? payload.request_id
    : typeof frame.request_id === "string"
      ? frame.request_id
      : "";

  if (requestId && (type === "ack" || type === "error")) {
    const pending = pendingCommands.get(requestId);
    if (pending) {
      pendingCommands.delete(requestId);
      if (type === "ack") {
        pending.resolve(payload);
      } else {
        const message = typeof payload.message === "string" && payload.message.trim()
          ? payload.message
          : "Realtime command failed";
        const code = typeof payload.code === "string" ? payload.code : "internal_error";
        pending.reject(new HttpError(`${code}: ${message}`, 409));
      }
    }
  }

  emitFrame(frame);
}

export function configureRealtimeClient(next: RealtimeClientConfig) {
  const nextUrl = String(next.url || "").trim() || null;
  const changed = nextUrl !== config.url || next.protocolVersion !== config.protocolVersion;
  config = {
    protocolVersion: next.protocolVersion ?? config.protocolVersion ?? DEFAULT_PROTOCOL_VERSION,
    url: nextUrl,
    heartbeatIntervalMs: next.heartbeatIntervalMs ?? config.heartbeatIntervalMs ?? DEFAULT_HEARTBEAT_INTERVAL_MS,
  };
  if (changed && socket) {
    disconnect();
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
  const nextNames = new Set(subscriptions.map((subscription) => subscription.name));
  for (const existingName of Array.from(desiredSubscriptions.keys())) {
    if (!nextNames.has(existingName)) {
      desiredSubscriptions.delete(existingName);
    }
  }
  for (const subscription of subscriptions) {
    desiredSubscriptions.set(subscription.name, subscription);
  }
  syncSubscriptions();
}

export async function connect() {
  if (connectPromise) {
    return connectPromise;
  }
  const url = resolveSocketUrl(config.url);
  if (!url || typeof window === "undefined" || typeof WebSocket === "undefined") {
    return;
  }

  shouldReconnect = true;
  emitState("connecting");
  connectPromise = new Promise<void>((resolve, reject) => {
    try {
      socket = new WebSocket(url);
    } catch (error) {
      connectPromise = null;
      emitState("error");
      reject(error);
      scheduleReconnect();
      return;
    }

    const currentSocket = socket;
    currentSocket.onopen = () => {
      connectPromise = null;
      emitState("open");
      startHeartbeatLoop();
      syncSubscriptions();
      resolve();
    };
    currentSocket.onerror = () => {
      emitState("error");
    };
    currentSocket.onclose = () => {
      const connectionLost = shouldReconnect;
      clearHeartbeatTimer();
      connectPromise = null;
      socket = null;
      emitState("closed");
      if (connectionLost) {
        rejectPendingCommands(new Error("Realtime socket disconnected"));
        scheduleReconnect();
      }
    };
    currentSocket.onmessage = (event) => {
      try {
        const frame = JSON.parse(String(event.data || "")) as RealtimeEnvelope;
        if (frame && typeof frame === "object") {
          handleFrame(frame);
        }
      } catch {
      }
    };
  });

  return connectPromise;
}

export function disconnect() {
  shouldReconnect = false;
  clearReconnectTimer();
  clearHeartbeatTimer();
  rejectPendingCommands(new Error("Realtime socket disconnected"));
  if (socket) {
    socket.close();
    socket = null;
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

  await connect();
  if (!socket || socket.readyState !== WebSocket.OPEN) {
    throw new Error("Realtime socket not connected");
  }

  nextRequestId += 1;
  const requestId = `req_${Date.now()}_${nextRequestId}`;

  return await new Promise<Record<string, unknown>>((resolve, reject) => {
    const timeoutId = typeof window === "undefined"
      ? null
      : window.setTimeout(() => {
        pendingCommands.delete(requestId);
        reject(new Error(`Realtime command timeout: ${command.type}`));
      }, DEFAULT_COMMAND_TIMEOUT_MS);

    pendingCommands.set(requestId, {
      resolve(payload) {
        if (timeoutId !== null && typeof window !== "undefined") {
          window.clearTimeout(timeoutId);
        }
        resolve(payload);
      },
      reject(error) {
        if (timeoutId !== null && typeof window !== "undefined") {
          window.clearTimeout(timeoutId);
        }
        reject(error);
      },
    });

    try {
      sendRaw({
        type: command.type,
        request_id: requestId,
        ts: Date.now() / 1000,
        stream: command.stream,
        payload: command.payload,
      });
    } catch (error) {
      pendingCommands.delete(requestId);
      if (timeoutId !== null && typeof window !== "undefined") {
        window.clearTimeout(timeoutId);
      }
      reject(error);
    }
  });
}
