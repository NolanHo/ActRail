import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  CancelQueueRequestSchema,
  CommandResponseSchema,
  EnqueueRequestSchema,
  EventEnvelopeSchema,
  InterruptRequestSchema,
  RespondUIRequestSchema,
  SendRequestSchema,
  SessionMessagesRequestSchema,
  SessionMessagesResponseSchema,
  SubscribeRequestSchema,
} from "../../gen/actrail/v1/transport_pb";
import { HttpError } from "../../lib/http";
import type { MessagesResponse, RealtimeEnvelope } from "../../lib/types";
import type { RealtimeCommand } from "./client";

const COMMAND_SERVICE = "actrail.v1.SessionCommandService";
const EVENT_SERVICE = "actrail.v1.EventService";

export type ConnectWireFormat = "json" | "proto";

export interface ConnectTransportConfig {
  basePath?: string | null;
  wireFormat?: ConnectWireFormat;
}

const textDecoder = new TextDecoder();

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

function bodyForCommandProto(command: RealtimeCommand, method: string) {
  const payload = command.payload || {};
  const session = sessionFromPayload(payload);
  if (method === "Send") {
    return toBinary(SendRequestSchema, create(SendRequestSchema, { session, text: typeof payload.text === "string" ? payload.text : "" }));
  }
  if (method === "Enqueue") {
    return toBinary(EnqueueRequestSchema, create(EnqueueRequestSchema, { session, text: typeof payload.text === "string" ? payload.text : "" }));
  }
  if (method === "CancelQueue") {
    return toBinary(CancelQueueRequestSchema, create(CancelQueueRequestSchema, { session }));
  }
  if (method === "Interrupt") {
    return toBinary(InterruptRequestSchema, create(InterruptRequestSchema, { session }));
  }
  if (method === "RespondUI") {
    const value = typeof payload.value === "string" ? payload.value : JSON.stringify(payload.value ?? "");
    return toBinary(RespondUIRequestSchema, create(RespondUIRequestSchema, {
      session,
      responseTo: typeof payload.response_to === "string" ? payload.response_to : "",
      value,
    }));
  }
  throw new Error(`Unsupported Connect command: ${command.type}`);
}

function decodeCommandResponseProto(bytes: Uint8Array) {
  return decodePayloadBytes(fromBinary(CommandResponseSchema, bytes).payloadJson);
}

function encodeSubscribeRequestProto(afterEventId: number) {
  return toBinary(SubscribeRequestSchema, create(SubscribeRequestSchema, { afterEventId: BigInt(Math.max(0, Math.floor(afterEventId))) }));
}

function decodeEventEnvelopeProto(bytes: Uint8Array): Record<string, unknown> {
  const frame = fromBinary(EventEnvelopeSchema, bytes);
  return {
    id: Number(frame.id),
    type: frame.type,
    stream: frame.stream,
    unixMillis: Number(frame.unixMillis),
    payload: decodePayloadBytes(frame.payloadJson),
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

export interface ConnectMessagesOptions {
  sessionId: string;
  after?: number;
  before?: number;
  limit?: number;
  init?: boolean;
  deferred?: boolean;
  activeTurnStartSeq?: number;
  includeToolDetails?: boolean;
  eventId?: string;
  toolCallId?: string;
  signal?: AbortSignal;
}

function messageResponseFromProto(data: ReturnType<typeof fromBinary<typeof SessionMessagesResponseSchema>>): MessagesResponse {
  const items = data.eventsJson.map((raw) => decodePayloadBytes(raw) as never);
  return {
    events: items,
    items,
    tail_seq: Number(data.tailSeq || 0n),
    has_more: data.hasMore,
    next_before_seq: data.nextBeforeSeq === undefined ? undefined : Number(data.nextBeforeSeq),
  };
}

export async function fetchConnectSessionMessages(config: ConnectTransportConfig, options: ConnectMessagesOptions): Promise<MessagesResponse> {
  const basePath = config.basePath || "/api/connect";
  const wireFormat = config.wireFormat === "json" ? "json" : "proto";
  const url = servicePath(basePath, COMMAND_SERVICE, "SessionMessages");
  if (wireFormat === "proto") {
    const body = toBinary(SessionMessagesRequestSchema, create(SessionMessagesRequestSchema, {
      sessionId: options.sessionId,
      ...(typeof options.after === "number" ? { afterSeq: BigInt(Math.floor(options.after)) } : {}),
      ...(typeof options.before === "number" ? { beforeSeq: BigInt(Math.floor(options.before)) } : {}),
      limit: options.limit || 0,
      init: options.init === true,
      deferred: options.deferred === true,
      activeTurnStartSeq: BigInt(options.activeTurnStartSeq || 0),
      includeToolDetails: options.includeToolDetails === true,
      eventId: options.eventId || "",
      toolCallId: options.toolCallId || "",
    }));
    const response = await fetch(apiPath(url), {
      method: "POST",
      headers: { "Content-Type": "application/connect+proto", Accept: "application/connect+proto" },
      body,
      signal: options.signal,
    });
    if (!response.ok) {
      throw new HttpError(await response.text(), response.status);
    }
    return messageResponseFromProto(fromBinary(SessionMessagesResponseSchema, new Uint8Array(await response.arrayBuffer())));
  }
  const response = await fetch(apiPath(url), {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({
      session: { session_id: options.sessionId },
      after_seq: options.after,
      before_seq: options.before,
      limit: options.limit,
      init: options.init,
      deferred: options.deferred,
      active_turn_start_seq: options.activeTurnStartSeq,
      include_tool_details: options.includeToolDetails,
      event_id: options.eventId,
      tool_call_id: options.toolCallId,
    }),
    signal: options.signal,
  });
  if (!response.ok) {
    throw new HttpError(await response.text(), response.status);
  }
  return await response.json() as MessagesResponse;
}
