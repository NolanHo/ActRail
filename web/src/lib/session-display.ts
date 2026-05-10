import type { SessionSummary } from "./types";

function nonEmptyText(value: unknown): string | null {
  if (typeof value !== "string") {
    return null;
  }
  const trimmed = value.trim();
  return trimmed || null;
}

function pathBaseName(value: string): string {
  const trimmed = value.trim();
  const normalized = trimmed.replace(/[\\/]+$/, "");
  if (!normalized) {
    return trimmed;
  }
  const slash = Math.max(normalized.lastIndexOf("/"), normalized.lastIndexOf("\\"));
  return slash >= 0 ? normalized.slice(slash + 1) || trimmed : normalized;
}

export function getSessionDisplayName(
  session: Pick<SessionSummary, "session_id" | "display_name" | "alias" | "title" | "cwd"> | null | undefined,
  fallback = "Session",
): string {
  if (!session) {
    return fallback;
  }
  const cwd = nonEmptyText(session.cwd);
  return nonEmptyText(session.display_name)
    || nonEmptyText(session.alias)
    || nonEmptyText(session.title)
    || (cwd ? pathBaseName(cwd) : null)
    || nonEmptyText(session.session_id)
    || fallback;
}
