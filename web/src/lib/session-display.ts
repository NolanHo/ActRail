import type { SessionSummary } from "./types";

function nonEmptyText(value: unknown): string | null {
  if (typeof value !== "string") {
    return null;
  }
  const trimmed = value.trim();
  return trimmed || null;
}

export function getSessionDisplayName(
  session: Pick<SessionSummary, "session_id" | "display_name" | "alias" | "title" | "cwd"> | null | undefined,
  fallback = "Session",
): string {
  if (!session) {
    return fallback;
  }
  return nonEmptyText(session.display_name)
    || nonEmptyText(session.alias)
    || nonEmptyText(session.title)
    || nonEmptyText(session.cwd)
    || nonEmptyText(session.session_id)
    || fallback;
}
