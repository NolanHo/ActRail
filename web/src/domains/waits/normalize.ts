import type { ActiveWaitSummary, WaitRecord, WaitState, WaitThreadSummary } from "../../lib/types";

const ACTIVE_STATES = new Set<WaitState>(["pending_unread", "claimed"]);

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" ? value as Record<string, unknown> : null;
}

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function numberOrNull(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value.trim());
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

export function isActiveWaitState(value: unknown): value is Extract<WaitState, "pending_unread" | "claimed"> {
  return ACTIVE_STATES.has(String(value || "") as WaitState);
}

export function normalizeWait(value: unknown): WaitRecord | null {
  const raw = record(value);
  if (!raw) {
    return null;
  }
  const waitId = text(raw.wait_id ?? raw.id);
  const threadId = text(raw.thread_id);
  const question = text(raw.question ?? raw.title ?? raw.prompt);
  const state = text(raw.state) as WaitState;
  if (!waitId || !threadId || !question || !state) {
    return null;
  }
  const files = Array.isArray(raw.files)
    ? raw.files.map((item) => text(item)).filter(Boolean)
    : Array.isArray(raw.files_json)
      ? raw.files_json.map((item) => text(item)).filter(Boolean)
      : undefined;
  return {
    wait_id: waitId,
    thread_id: threadId,
    session_id: text(raw.session_id) || undefined,
    state,
    question,
    context: text(raw.context) || undefined,
    blocking_reason: text(raw.blocking_reason) || undefined,
    attempted: text(raw.attempted) || undefined,
    default_if_no_reply: text(raw.default_if_no_reply) || undefined,
    answer: text(raw.answer) || undefined,
    fallback_used: text(raw.fallback_used) || undefined,
    files,
    claimed_at: numberOrNull(raw.claimed_at),
    answered_at: numberOrNull(raw.answered_at),
    cancelled_at: numberOrNull(raw.cancelled_at),
    timed_out_at: numberOrNull(raw.timed_out_at),
    orphaned_at: numberOrNull(raw.orphaned_at),
    timeout_at: numberOrNull(raw.timeout_at),
    created_at: numberOrNull(raw.created_at),
    updated_at: numberOrNull(raw.updated_at),
  };
}

export function normalizeActiveWait(value: unknown, sessionId?: string): ActiveWaitSummary | null {
  const wait = normalizeWait(value);
  if (!wait) {
    return null;
  }
  return {
    wait_id: wait.wait_id,
    thread_id: wait.thread_id,
    session_id: wait.session_id || sessionId,
    state: wait.state,
    question: wait.question,
    blocking_reason: wait.blocking_reason,
    attempted: wait.attempted,
    default_if_no_reply: wait.default_if_no_reply,
    claimed_at: wait.claimed_at,
    timeout_at: wait.timeout_at,
    created_at: wait.created_at,
    updated_at: wait.updated_at,
  };
}

export function normalizeThread(value: unknown, sessionId?: string): WaitThreadSummary | null {
  const raw = record(value);
  if (!raw) {
    return null;
  }
  const threadId = text(raw.thread_id ?? raw.id);
  const resolvedSessionId = text(raw.session_id) || sessionId || "";
  if (!threadId || !resolvedSessionId) {
    return null;
  }
  return {
    thread_id: threadId,
    session_id: resolvedSessionId,
    title: text(raw.title) || undefined,
    active_wait: normalizeActiveWait(raw.active_wait, resolvedSessionId),
    created_at: numberOrNull(raw.created_at),
    updated_at: numberOrNull(raw.updated_at),
    closed_at: numberOrNull(raw.closed_at),
    wait_count: typeof raw.wait_count === "number" && Number.isFinite(raw.wait_count) ? raw.wait_count : undefined,
  };
}

export function waitStateLabel(state: string | undefined) {
  switch (state) {
    case "pending_unread":
      return "Waiting on user";
    case "claimed":
      return "Claimed";
    case "answered":
      return "Answered";
    case "timed_out_locked":
      return "Timed out";
    case "cancelled":
      return "Cancelled";
    case "orphaned":
      return "Orphaned";
    default:
      return state || "Wait";
  }
}
