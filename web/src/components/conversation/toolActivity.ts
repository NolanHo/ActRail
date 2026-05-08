import type { MessageEvent } from "../../lib/types";

export type MachineTraceKind = "reasoning" | "tool" | "tool_result" | "todo_snapshot" | "custom_message" | "pi_event";

export interface ToolActivityEvent {
  event: MessageEvent;
  index: number;
  kind: MachineTraceKind;
  key: string;
  ts: number | null;
  priority: number;
}

export interface ToolActivitySummary {
  totalTools: number;
  toolCalls: number;
  toolResults: number;
  running: number;
  ok: number;
  failed: number;
  reasoning: number;
  todoSnapshots: number;
  processUpdates: number;
  systemEvents: number;
  startedAt: number | null;
  lastActivityAt: number | null;
  elapsedSeconds: number | null;
  lastActivityAgeSeconds: number | null;
  maxRunningSeconds: number | null;
  stalled: boolean;
  runningToolNames: string[];
  visibleEvents: ToolActivityEvent[];
  hiddenEventCount: number;
  summaryText: string;
  statusText: string;
}

export interface ToolActivityOptions {
  nowSeconds?: number;
  isBusy?: boolean;
  visibleLimit?: number;
  staleAfterSeconds?: number;
  kindForEvent: (event: MessageEvent) => MachineTraceKind | null;
  eventKey: (event: MessageEvent, kind: MachineTraceKind, index: number) => string;
  eventTimestampSeconds: (event: MessageEvent) => number | null;
  toolCallID: (event: MessageEvent) => string;
  toolName: (event: MessageEvent) => string;
  piEventVariant?: (event: MessageEvent) => string | null;
}

interface ToolCallState {
  id: string;
  name: string;
  startIndex: number;
  startTs: number | null;
  resultIndex: number | null;
  resultTs: number | null;
  failed: boolean;
}

const DEFAULT_VISIBLE_LIMIT = 12;
const DEFAULT_STALE_AFTER_SECONDS = 30;

function formatCompactDuration(seconds: number | null): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) {
    return "";
  }
  const total = Math.max(0, Math.floor(seconds));
  if (total < 60) {
    return `${total}s`;
  }
  const minutes = Math.floor(total / 60);
  if (minutes < 60) {
    return `${minutes}m${total % 60 ? `${total % 60}s` : ""}`;
  }
  const hours = Math.floor(minutes / 60);
  return `${hours}h${minutes % 60 ? `${minutes % 60}m` : ""}`;
}

function uniqueFirst(values: string[], limit: number): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const value of values) {
    const normalized = value.trim();
    if (!normalized || seen.has(normalized)) {
      continue;
    }
    seen.add(normalized);
    out.push(normalized);
    if (out.length >= limit) {
      break;
    }
  }
  return out;
}

function fallbackCallID(index: number): string {
  return `fallback:${index}`;
}

function pluralize(count: number, singular: string, plural = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : plural}`;
}

function eventPriority(event: MessageEvent, kind: MachineTraceKind, index: number, runningIndexes: Set<number>, piEventVariant?: string | null): number {
  if (runningIndexes.has(index)) {
    return 120;
  }
  if (Boolean(event.is_error) || piEventVariant === "retry_error" || piEventVariant === "empty_output" || piEventVariant === "turn_terminal") {
    return 110;
  }
  if (kind === "pi_event") {
    return 95;
  }
  if (kind === "tool" || kind === "tool_result") {
    return 80;
  }
  if (kind === "todo_snapshot" || kind === "custom_message") {
    return 65;
  }
  return 50;
}

function sortByRecencyThenIndex(left: ToolActivityEvent, right: ToolActivityEvent): number {
  const leftTs = left.ts ?? -Infinity;
  const rightTs = right.ts ?? -Infinity;
  if (left.priority !== right.priority) {
    return right.priority - left.priority;
  }
  if (leftTs !== rightTs) {
    return rightTs - leftTs;
  }
  return right.index - left.index;
}

export function buildToolActivitySummary(events: MessageEvent[], options: ToolActivityOptions): ToolActivitySummary {
  const nowSeconds = options.nowSeconds ?? Date.now() / 1000;
  const visibleLimit = Math.max(1, Math.floor(options.visibleLimit ?? DEFAULT_VISIBLE_LIMIT));
  const staleAfterSeconds = Math.max(1, options.staleAfterSeconds ?? DEFAULT_STALE_AFTER_SECONDS);
  const isBusy = options.isBusy === true;

  const machineEvents: ToolActivityEvent[] = [];
  const callsByID = new Map<string, ToolCallState>();
  const pendingWithoutID: ToolCallState[] = [];
  let toolCalls = 0;
  let toolResults = 0;
  let reasoning = 0;
  let todoSnapshots = 0;
  let processUpdates = 0;
  let systemEvents = 0;
  let startedAt: number | null = null;
  let lastActivityAt: number | null = null;

  events.forEach((event, index) => {
    const kind = options.kindForEvent(event);
    if (!kind) {
      return;
    }
    const ts = options.eventTimestampSeconds(event);
    if (ts !== null) {
      startedAt = startedAt === null ? ts : Math.min(startedAt, ts);
      lastActivityAt = lastActivityAt === null ? ts : Math.max(lastActivityAt, ts);
    }
    machineEvents.push({
      event,
      index,
      kind,
      key: options.eventKey(event, kind, index),
      ts,
      priority: 0,
    });

    if (kind === "reasoning") {
      reasoning += 1;
      return;
    }
    if (kind === "todo_snapshot") {
      todoSnapshots += 1;
      return;
    }
    if (kind === "custom_message") {
      processUpdates += 1;
      return;
    }
    if (kind === "pi_event") {
      systemEvents += 1;
      return;
    }

    const toolID = options.toolCallID(event);
    if (kind === "tool") {
      toolCalls += 1;
      const state: ToolCallState = {
        id: toolID || fallbackCallID(index),
        name: options.toolName(event),
        startIndex: index,
        startTs: ts,
        resultIndex: null,
        resultTs: null,
        failed: false,
      };
      if (toolID) {
        callsByID.set(toolID, state);
      } else {
        pendingWithoutID.push(state);
      }
      return;
    }

    toolResults += 1;
    let call: ToolCallState | undefined;
    if (toolID) {
      call = callsByID.get(toolID);
      if (!call) {
        call = {
          id: toolID,
          name: options.toolName(event),
          startIndex: index,
          startTs: null,
          resultIndex: null,
          resultTs: null,
          failed: false,
        };
        callsByID.set(toolID, call);
      }
    } else {
      call = pendingWithoutID.find((candidate) => candidate.resultIndex === null);
      if (!call) {
        call = {
          id: fallbackCallID(index),
          name: options.toolName(event),
          startIndex: index,
          startTs: null,
          resultIndex: null,
          resultTs: null,
          failed: false,
        };
        pendingWithoutID.push(call);
      }
    }
    call.resultIndex = index;
    call.resultTs = ts;
    call.failed = event.is_error === true;
    if (!call.name) {
      call.name = options.toolName(event);
    }
  });

  const calls = [...callsByID.values(), ...pendingWithoutID];
  const toolRuns = calls.length;
  const runningCalls = isBusy ? calls.filter((call) => call.resultIndex === null && call.startIndex >= 0) : [];
  const runningIndexes = new Set(runningCalls.map((call) => call.startIndex));
  let failed = calls.filter((call) => call.resultIndex !== null && call.failed).length;
  failed += machineEvents.filter((item) => item.kind === "pi_event" && item.event.is_error === true).length;
  const ok = calls.filter((call) => call.resultIndex !== null && !call.failed).length;
  const maxRunningSeconds = runningCalls.reduce<number | null>((max, call) => {
    if (call.startTs === null) {
      return max;
    }
    const elapsed = Math.max(0, nowSeconds - call.startTs);
    return max === null ? elapsed : Math.max(max, elapsed);
  }, null);
  const lastActivityAgeSeconds = lastActivityAt === null ? null : Math.max(0, nowSeconds - lastActivityAt);
  const elapsedSeconds = startedAt === null
    ? null
    : Math.max(0, (isBusy ? nowSeconds : lastActivityAt ?? nowSeconds) - startedAt);
  const stalled = Boolean(isBusy && runningCalls.length > 0 && lastActivityAgeSeconds !== null && lastActivityAgeSeconds >= staleAfterSeconds);

  const visibleEvents = machineEvents
    .map((item) => {
      const piVariant = item.kind === "pi_event" ? options.piEventVariant?.(item.event) ?? null : null;
      return {
        ...item,
        priority: eventPriority(item.event, item.kind, item.index, runningIndexes, piVariant),
      };
    })
    .sort(sortByRecencyThenIndex)
    .slice(0, visibleLimit)
    .sort((left, right) => left.index - right.index);

  const runningToolNames = uniqueFirst(runningCalls.map((call) => call.name || "tool"), 3);
  const parts: string[] = [];
  if (toolRuns > 0) {
    parts.push(runningCalls.length > 0
      ? `Running ${runningCalls.length}/${toolRuns} ${toolRuns === 1 ? "tool" : "tools"}`
      : `Ran ${pluralize(toolRuns, "tool")}`);
  } else {
    parts.push("Activity");
  }
  if (ok) {
    parts.push(`${ok} ok`);
  }
  if (failed) {
    parts.push(`${failed} failed`);
  }
  if (reasoning) {
    parts.push(`${reasoning} reasoning`);
  }
  if (todoSnapshots) {
    parts.push(`${todoSnapshots} todo`);
  }
  if (processUpdates) {
    parts.push(`${processUpdates} process`);
  }
  if (systemEvents) {
    parts.push(`${systemEvents} system`);
  }
  if (elapsedSeconds !== null) {
    parts.push(formatCompactDuration(elapsedSeconds));
  }
  if (stalled && lastActivityAgeSeconds !== null) {
    parts.push(`no output ${formatCompactDuration(lastActivityAgeSeconds)}`);
  } else if (lastActivityAgeSeconds !== null && isBusy) {
    parts.push(`last ${formatCompactDuration(lastActivityAgeSeconds)} ago`);
  }

  const statusParts: string[] = [];
  if (runningCalls.length) {
    statusParts.push(`running ${runningCalls.length}${runningToolNames.length ? `: ${runningToolNames.join(", ")}` : ""}`);
  }
  if (failed) {
    statusParts.push(`${failed} failed`);
  }
  if (!statusParts.length) {
    statusParts.push(machineEvents.length ? "complete" : "no activity");
  }

  return {
    totalTools: toolRuns,
    toolCalls,
    toolResults,
    running: runningCalls.length,
    ok,
    failed,
    reasoning,
    todoSnapshots,
    processUpdates,
    systemEvents,
    startedAt,
    lastActivityAt,
    elapsedSeconds,
    lastActivityAgeSeconds,
    maxRunningSeconds,
    stalled,
    runningToolNames,
    visibleEvents,
    hiddenEventCount: Math.max(0, machineEvents.length - visibleEvents.length),
    summaryText: parts.join(" · "),
    statusText: statusParts.join(" · "),
  };
}
