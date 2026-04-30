import { HttpError } from "../../lib/http";
import type { RealtimeEnvelope } from "../../lib/types";
import type { RealtimeCommand } from "./client";

const COMMAND_SERVICE = "actrail.v1.SessionCommandService";
const EVENT_SERVICE = "actrail.v1.EventService";

export type ConnectWireFormat = "json" | "proto";

export interface ConnectTransportConfig {
  basePath?: string | null;
  wireFormat?: ConnectWireFormat;
}

interface ProtoField {
  num: number;
  wire: number;
  value?: Uint8Array;
  u64?: number;
}

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

function concatBytes(...parts: Uint8Array[]) {
  const length = parts.reduce((sum, part) => sum + part.length, 0);
  const out = new Uint8Array(length);
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

function protoVarint(value: number) {
  const bytes: number[] = [];
  let n = Math.max(0, Math.floor(value));
  while (n > 0x7f) {
    bytes.push((n & 0x7f) | 0x80);
    n = Math.floor(n / 128);
  }
  bytes.push(n);
  return new Uint8Array(bytes);
}

function protoKey(field: number, wire: number) {
  return protoVarint(field * 8 + wire);
}

function protoString(field: number, value: unknown) {
  const text = typeof value === "string" ? value : "";
  if (!text) return new Uint8Array(0);
  const bytes = textEncoder.encode(text);
  return concatBytes(protoKey(field, 2), protoVarint(bytes.length), bytes);
}

function protoBytes(field: number, bytes: Uint8Array) {
  if (!bytes.length) return new Uint8Array(0);
  return concatBytes(protoKey(field, 2), protoVarint(bytes.length), bytes);
}

function protoMessage(field: number, bytes: Uint8Array) {
  return protoBytes(field, bytes);
}

function protoUint64(field: number, value: number) {
  if (!Number.isFinite(value) || value <= 0) return new Uint8Array(0);
  return concatBytes(protoKey(field, 0), protoVarint(value));
}

function readVarint(data: Uint8Array, offset: number): { value: number; offset: number } {
  let value = 0;
  let shift = 0;
  while (offset < data.length) {
    const byte = data[offset++];
    value += (byte & 0x7f) * 2 ** shift;
    if ((byte & 0x80) === 0) return { value, offset };
    shift += 7;
  }
  throw new Error("invalid protobuf varint");
}

function readProtoFields(data: Uint8Array): ProtoField[] {
  const fields: ProtoField[] = [];
  let offset = 0;
  while (offset < data.length) {
    const key = readVarint(data, offset);
    offset = key.offset;
    const num = Math.floor(key.value / 8);
    const wire = key.value % 8;
    if (wire === 0) {
      const value = readVarint(data, offset);
      offset = value.offset;
      fields.push({ num, wire, u64: value.value });
    } else if (wire === 2) {
      const length = readVarint(data, offset);
      offset = length.offset;
      const end = offset + length.value;
      if (end > data.length) throw new Error("invalid protobuf bytes length");
      fields.push({ num, wire, value: data.slice(offset, end) });
      offset = end;
    } else {
      throw new Error(`unsupported protobuf wire type ${wire}`);
    }
  }
  return fields;
}

function fieldBytes(fields: ProtoField[], num: number) {
  return fields.find((field) => field.num === num && field.wire === 2)?.value ?? new Uint8Array(0);
}

function fieldString(fields: ProtoField[], num: number) {
  const bytes = fieldBytes(fields, num);
  return bytes.length ? textDecoder.decode(bytes) : "";
}

function fieldNumber(fields: ProtoField[], num: number) {
  return fields.find((field) => field.num === num && field.wire === 0)?.u64 ?? 0;
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
  const json = textDecoder.decode(bytes);
  return json ? JSON.parse(json) as Record<string, unknown> : {};
}

function decodePayloadBytes(raw: Uint8Array) {
  const json = raw.length ? textDecoder.decode(raw) : "";
  return json ? JSON.parse(json) as Record<string, unknown> : {};
}

function encodeSessionProto(session: ReturnType<typeof sessionFromPayload>) {
  return concatBytes(protoString(1, session.sessionId), protoString(2, session.runtimeId));
}

function bodyForCommandProto(command: RealtimeCommand, method: string) {
  const payload = command.payload || {};
  const session = protoMessage(1, encodeSessionProto(sessionFromPayload(payload)));
  if (method === "Send" || method === "Enqueue") {
    return concatBytes(session, protoString(2, payload.text));
  }
  if (method === "RespondUI") {
    const value = typeof payload.value === "string" ? payload.value : JSON.stringify(payload.value ?? "");
    return concatBytes(session, protoString(2, payload.response_to), protoString(3, value));
  }
  return session;
}

function decodeCommandResponseProto(bytes: Uint8Array) {
  return decodePayloadBytes(fieldBytes(readProtoFields(bytes), 1));
}

function encodeSubscribeRequestProto(afterEventId: number) {
  return protoUint64(1, afterEventId);
}

function decodeEventEnvelopeProto(bytes: Uint8Array): Record<string, unknown> {
  const fields = readProtoFields(bytes);
  return {
    id: fieldNumber(fields, 1),
    type: fieldString(fields, 2),
    stream: fieldString(fields, 3),
    unixMillis: fieldNumber(fields, 4),
    payload: decodePayloadBytes(fieldBytes(fields, 5)),
  };
}

export async function sendConnectCommand(config: ConnectTransportConfig, command: RealtimeCommand) {
  const started = performance.now();
  const basePath = config.basePath || "/api/connect";
  const method = methodForCommand(command.type);
  const proto = config.wireFormat === "proto";
  const response = await fetch(apiPath(servicePath(basePath, COMMAND_SERVICE, method)), {
    method: "POST",
    headers: proto
      ? { "Content-Type": "application/connect+proto", Accept: "application/connect+proto" }
      : { "Content-Type": "application/json", Accept: "application/json" },
    body: proto ? bodyForCommandProto(command, method) : JSON.stringify(bodyForCommand(command)),
  });
  const latencyMs = Math.round(performance.now() - started);
  if (!response.ok) {
    const data = await response.json().catch(() => ({})) as { message?: string; code?: string };
    console.debug("actrail.connect.command", { method, latency_ms: latencyMs, ok: false, status: response.status, wire_format: proto ? "proto" : "json" });
    throw new HttpError(data.message || data.code || `Connect command failed: ${response.status}`, response.status);
  }
  console.debug("actrail.connect.command", { method, latency_ms: latencyMs, ok: true, wire_format: proto ? "proto" : "json" });
  if (proto) {
    return decodeCommandResponseProto(new Uint8Array(await response.arrayBuffer()));
  }
  const data = await response.json().catch(() => ({})) as { payloadJson?: unknown };
  return decodePayloadJson(data.payloadJson);
}

export function frameToRealtimeEnvelope(frame: Record<string, unknown>): RealtimeEnvelope | null {
  const type = typeof frame.type === "string" ? frame.type : "";
  const stream = typeof frame.stream === "string" ? frame.stream : "";
  if (!type || !stream) {
    return null;
  }
  const payload = "payload" in frame && frame.payload && typeof frame.payload === "object"
    ? frame.payload as Record<string, unknown>
    : decodePayloadJson(frame.payloadJson);
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

export async function readConnectStream(response: Response, onFrame: (frame: Record<string, unknown>) => void, signal: AbortSignal, wireFormat: ConnectWireFormat = "json") {
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
      const frame = wireFormat === "proto"
        ? decodeEventEnvelopeProto(payload)
        : JSON.parse(textDecoder.decode(payload)) as Record<string, unknown>;
      const unixMillis = typeof frame.unixMillis === "number" ? frame.unixMillis : Number(frame.unixMillis || 0);
      const lagMs = Number.isFinite(unixMillis) && unixMillis > 0 ? Math.max(0, Date.now() - unixMillis) : null;
      console.debug("actrail.connect.event", { id: frame.id, type: frame.type, stream: frame.stream, lag_ms: lagMs, wire_format: wireFormat });
      onFrame(frame);
    }
  }
}

export function subscribeConnectEvents(config: ConnectTransportConfig, afterEventId: number, onFrame: (frame: Record<string, unknown>) => void, signal: AbortSignal) {
  const basePath = config.basePath || "/api/connect";
  const wireFormat = config.wireFormat === "proto" ? "proto" : "json";
  return fetch(apiPath(servicePath(basePath, EVENT_SERVICE, "Subscribe")), {
    method: "POST",
    signal,
    headers: wireFormat === "proto"
      ? { "Content-Type": "application/connect+proto", Accept: "application/connect+proto" }
      : { "Content-Type": "application/json", Accept: "application/connect+json" },
    body: wireFormat === "proto" ? encodeSubscribeRequestProto(afterEventId) : JSON.stringify({ afterEventId }),
  }).then((response) => {
    if (!response.ok) {
      throw new HttpError(`Connect event stream failed: ${response.status}`, response.status);
    }
    return readConnectStream(response, onFrame, signal, wireFormat);
  });
}
