import { HttpError } from "../../lib/http";
import type { RealtimeEnvelope } from "../../lib/types";
import type { RealtimeCommand } from "./client";

const COMMAND_SERVICE = "actrail.v1.SessionCommandService";
const EVENT_SERVICE = "actrail.v1.EventService";

export interface ConnectTransportConfig {
  basePath?: string | null;
}

function apiPath(path: string) {
  return path.startsWith("/") ? path.slice(1) : path;
}

function servicePath(basePath: string, service: string, method: string) {
  return `${basePath.replace(/\/+$/, "")}/${service}/${method}`;
}

function sessionFromPayload(payload: Record<string, unknown>) {
  const sessionId = typeof payload.session_id === "string" ? payload.session_id : "";
  const runtimeId = typeof payload.runtime_id === "string" ? payload.runtime_id : "";
  return { sessionId, ...(runtimeId ? { runtimeId } : {}) };
}

function methodForCommand(type: string) {
  switch (type) {
    case "send":
      return "Send";
    case "enqueue":
      return "Enqueue";
    case "queue.cancel":
      return "CancelQueue";
    case "interrupt":
      return "Interrupt";
    case "ui.response":
      return "RespondUI";
    default:
      throw new Error(`Unsupported Connect command: ${type}`);
  }
}

function bodyForCommand(command: RealtimeCommand) {
  const payload = command.payload || {};
  const body: Record<string, unknown> = { session: sessionFromPayload(payload) };
  if (command.type === "send" || command.type === "enqueue") {
    body.text = payload.text;
  }
  if (command.type === "ui.response") {
    body.responseTo = payload.response_to;
    body.value = payload.value;
  }
  return body;
}

function decodePayloadJson(raw: unknown) {
  if (typeof raw !== "string" || !raw.trim()) {
    return {};
  }
  const binary = atob(raw);
  const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
  const json = new TextDecoder().decode(bytes);
  return json ? JSON.parse(json) as Record<string, unknown> : {};
}

export async function sendConnectCommand(config: ConnectTransportConfig, command: RealtimeCommand) {
  const started = performance.now();
  const basePath = config.basePath || "/api/connect";
  const method = methodForCommand(command.type);
  const response = await fetch(apiPath(servicePath(basePath, COMMAND_SERVICE, method)), {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
    },
    body: JSON.stringify(bodyForCommand(command)),
  });
  const data = await response.json().catch(() => ({})) as { payloadJson?: unknown; message?: string; code?: string };
  const latencyMs = Math.round(performance.now() - started);
  if (!response.ok) {
    console.debug("actrail.connect.command", { method, latency_ms: latencyMs, ok: false, status: response.status });
    throw new HttpError(data.message || data.code || `Connect command failed: ${response.status}`, response.status);
  }
  console.debug("actrail.connect.command", { method, latency_ms: latencyMs, ok: true });
  return decodePayloadJson(data.payloadJson);
}

export function frameToRealtimeEnvelope(frame: Record<string, unknown>): RealtimeEnvelope | null {
  const type = typeof frame.type === "string" ? frame.type : "";
  const stream = typeof frame.stream === "string" ? frame.stream : "";
  if (!type || !stream) {
    return null;
  }
  const payload = decodePayloadJson(frame.payloadJson);
  const unixMillis = typeof frame.unixMillis === "number"
    ? frame.unixMillis
    : typeof frame.unixMillis === "string"
      ? Number(frame.unixMillis)
      : Date.now();
  return {
    id: typeof frame.id === "number" ? String(frame.id) : typeof frame.id === "string" ? frame.id : undefined,
    type,
    stream,
    ts: Number.isFinite(unixMillis) ? unixMillis / 1000 : Date.now() / 1000,
    payload,
  } as RealtimeEnvelope;
}

export async function readConnectStream(response: Response, onFrame: (frame: Record<string, unknown>) => void, signal: AbortSignal) {
  const reader = response.body?.getReader();
  if (!reader) {
    return;
  }
  let buffer = new Uint8Array(0);
  const append = (chunk: Uint8Array) => {
    const next = new Uint8Array(buffer.length + chunk.length);
    next.set(buffer, 0);
    next.set(chunk, buffer.length);
    buffer = next;
  };
  while (!signal.aborted) {
    const { done, value } = await reader.read();
    if (done) break;
    if (value) append(value);
    while (buffer.length >= 5) {
      const len = new DataView(buffer.buffer, buffer.byteOffset + 1, 4).getUint32(0, false);
      if (buffer.length < 5 + len) break;
      const payload = buffer.slice(5, 5 + len);
      buffer = buffer.slice(5 + len);
      const text = new TextDecoder().decode(payload);
      if (text) {
        const frame = JSON.parse(text) as Record<string, unknown>;
        const unixMillis = typeof frame.unixMillis === "number" ? frame.unixMillis : Number(frame.unixMillis || 0);
        const lagMs = Number.isFinite(unixMillis) && unixMillis > 0 ? Math.max(0, Date.now() - unixMillis) : null;
        console.debug("actrail.connect.event", { id: frame.id, type: frame.type, stream: frame.stream, lag_ms: lagMs });
        onFrame(frame);
      }
    }
  }
}

export function subscribeConnectEvents(config: ConnectTransportConfig, afterEventId: number, onFrame: (frame: Record<string, unknown>) => void, signal: AbortSignal) {
  const basePath = config.basePath || "/api/connect";
  return fetch(apiPath(servicePath(basePath, EVENT_SERVICE, "Subscribe")), {
    method: "POST",
    signal,
    headers: {
      "Content-Type": "application/json",
      Accept: "application/connect+json",
    },
    body: JSON.stringify({ afterEventId }),
  }).then((response) => {
    if (!response.ok) {
      throw new HttpError(`Connect event stream failed: ${response.status}`, response.status);
    }
    return readConnectStream(response, onFrame, signal);
  });
}
