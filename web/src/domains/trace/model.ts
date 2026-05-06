import type { MessageEvent } from "../../lib/types";

export type TraceEdgeConfidence = "explicit" | "inferred";
export type TraceNodeKind = "message" | "tool" | "event" | "group";
export type TraceNodeStatus = "running" | "pass" | "fail" | "cancelled";

export interface TraceNode {
  id: string;
  kind: TraceNodeKind;
  label: string;
  summary: string;
  status?: TraceNodeStatus;
  edgeConfidence: TraceEdgeConfidence;
  seq?: number;
  ts?: number;
  eventId?: string;
  parentEventId?: string;
  toolCallId?: string;
  call?: MessageEvent;
  result?: MessageEvent;
  event?: MessageEvent;
  durationSeconds?: number;
  children: TraceNode[];
}

interface NodeRecord {
  node: TraceNode;
  parentEventId: string;
  index: number;
}

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

export function eventKind(event: MessageEvent | undefined): string {
  if (!event) {
    return "";
  }
  return text(event.type) || text(event.kind) || text(event.role) || "event";
}

export function eventId(event: MessageEvent | undefined): string {
  return text(event?.event_id);
}

export function parentEventId(event: MessageEvent | undefined): string {
  return text(event?.parent_event_id);
}

export function toolCallId(event: MessageEvent | undefined): string {
  if (!event) {
    return "";
  }
  const direct = text(event.tool_call_id);
  if (direct) {
    return direct;
  }
  const details = event.details && typeof event.details === "object" ? event.details as Record<string, unknown> : null;
  return text(details?.tool_call_id);
}

function nodeSeq(...events: Array<MessageEvent | undefined>): number | undefined {
  return events.map((event) => numberValue(event?.seq)).find((seq) => seq !== undefined);
}

function nodeTs(...events: Array<MessageEvent | undefined>): number | undefined {
  return events.map((event) => numberValue(event?.ts)).find((ts) => ts !== undefined);
}

function firstEventId(...events: Array<MessageEvent | undefined>): string {
  return events.map(eventId).find(Boolean) || "";
}

function firstParentEventId(...events: Array<MessageEvent | undefined>): string {
  return events.map(parentEventId).find(Boolean) || "";
}

function eventSummary(event: MessageEvent | undefined): string {
  if (!event) {
    return "";
  }
  return text(event.summary)
    || text(event.name)
    || text(event.subject)
    || text(event.description)
    || text(event.text)
    || text(event.question)
    || text(event.operation)
    || eventKind(event);
}

function short(value: string, limit = 160): string {
  return value.length > limit ? `${value.slice(0, limit - 1)}...` : value;
}

function toolName(event: MessageEvent | undefined): string {
  if (!event) {
    return "tool";
  }
  const details = event.details && typeof event.details === "object" ? event.details as Record<string, unknown> : null;
  return text(event.name) || text(event.toolName) || text(details?.name) || "tool";
}

function isCancelled(event: MessageEvent | undefined): boolean {
  return event?.cancelled === true || text(event?.request_state) === "cancelled" || eventKind(event) === "cancelled";
}

function isError(event: MessageEvent | undefined): boolean {
  const kind = eventKind(event);
  return event?.is_error === true || kind === "error" || kind === "tool_error";
}

function toolStatus(call: MessageEvent | undefined, result: MessageEvent | undefined): TraceNodeStatus {
  if (isCancelled(call) || isCancelled(result)) {
    return "cancelled";
  }
  if (!result) {
    return "running";
  }
  return isError(result) ? "fail" : "pass";
}

function durationSeconds(call: MessageEvent | undefined, result: MessageEvent | undefined): number | undefined {
  const start = numberValue(call?.ts);
  const end = numberValue(result?.ts);
  if (start === undefined || end === undefined) {
    return undefined;
  }
  return Math.max(0, end - start);
}

function nodeId(prefix: string, event: MessageEvent | undefined, fallbackIndex: number): string {
  const eid = eventId(event);
  if (eid) {
    return `${prefix}:event:${eid}`;
  }
  const seq = numberValue(event?.seq);
  if (seq !== undefined) {
    return `${prefix}:seq:${Math.floor(seq)}`;
  }
  return `${prefix}:index:${fallbackIndex}`;
}

function makeToolRecord(call: MessageEvent | undefined, result: MessageEvent | undefined, id: string, index: number): NodeRecord {
  const status = toolStatus(call, result);
  const name = toolName(call) || toolName(result);
  const parentId = firstParentEventId(call, result);
  const explicit = parentId ? "explicit" : "inferred";
  return {
    index,
    parentEventId: parentId,
    node: {
      id: `tool:${id}`,
      kind: "tool",
      label: name,
      summary: short(eventSummary(result) || eventSummary(call) || name),
      status,
      edgeConfidence: explicit,
      seq: nodeSeq(call, result),
      ts: nodeTs(call, result),
      eventId: firstEventId(call, result) || undefined,
      parentEventId: parentId || undefined,
      toolCallId: id,
      call,
      result,
      durationSeconds: durationSeconds(call, result),
      children: [],
    },
  };
}

function makeEventRecord(event: MessageEvent, index: number): NodeRecord {
  const parentId = parentEventId(event);
  const kind = eventKind(event);
  const role = text(event.role);
  const label = role && (role === "user" || role === "assistant") ? role : kind;
  return {
    index,
    parentEventId: parentId,
    node: {
      id: nodeId("event", event, index),
      kind: role === "user" || role === "assistant" ? "message" : "event",
      label,
      summary: short(eventSummary(event)),
      status: isCancelled(event) ? "cancelled" : isError(event) ? "fail" : undefined,
      edgeConfidence: parentId ? "explicit" : "inferred",
      seq: numberValue(event.seq),
      ts: numberValue(event.ts),
      eventId: eventId(event) || undefined,
      parentEventId: parentId || undefined,
      toolCallId: toolCallId(event) || undefined,
      event,
      children: [],
    },
  };
}

export function buildTrace(events: MessageEvent[]): TraceNode[] {
  const toolCalls = new Map<string, { event: MessageEvent; index: number }>();
  const toolResults = new Map<string, { event: MessageEvent; index: number }>();
  const consumed = new Set<number>();

  events.forEach((event, index) => {
    const id = toolCallId(event);
    if (!id) {
      return;
    }
    const kind = eventKind(event);
    if (kind === "tool") {
      toolCalls.set(id, { event, index });
      consumed.add(index);
    } else if (kind === "tool_result") {
      toolResults.set(id, { event, index });
      consumed.add(index);
    }
  });

  const ids = new Set([...toolCalls.keys(), ...toolResults.keys()]);
  const records: NodeRecord[] = [];
  for (const id of ids) {
    const call = toolCalls.get(id);
    const result = toolResults.get(id);
    records.push(makeToolRecord(call?.event, result?.event, id, Math.min(call?.index ?? Number.MAX_SAFE_INTEGER, result?.index ?? Number.MAX_SAFE_INTEGER)));
  }

  events.forEach((event, index) => {
    if (consumed.has(index)) {
      return;
    }
    records.push(makeEventRecord(event, index));
  });

  records.sort((a, b) => a.index - b.index);
  const byEventId = new Map<string, TraceNode>();
  for (const record of records) {
    for (const id of [record.node.eventId, eventId(record.node.call), eventId(record.node.result), eventId(record.node.event)]) {
      if (id) {
        byEventId.set(id, record.node);
      }
    }
  }

  const roots: TraceNode[] = [];
  for (const record of records) {
    const parent = record.parentEventId ? byEventId.get(record.parentEventId) : undefined;
    if (parent && parent.id !== record.node.id) {
      parent.children.push(record.node);
    } else {
      roots.push(record.node);
    }
  }
  return roots;
}
