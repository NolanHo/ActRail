import { useEffect, useMemo, useState } from "preact/hooks";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { api } from "../../lib/api";
import { cn } from "../../lib/utils";
import type { CodexSessionFileResponse, CodexSessionFileSummary, CodexSessionFileTurn, MessageEvent } from "../../lib/types";

type SessionFileScope = "all" | "cwd";

interface SessionFileViewProps {
  open: boolean;
  activeCwd?: string;
  onClose(): void;
  onRenamed?(): void;
}

function titleForSummary(summary: CodexSessionFileSummary | null | undefined) {
  return summary?.display_name || summary?.title || summary?.first_user_message || summary?.thread_id || "Untitled";
}

function formatSessionDate(ts: number | undefined) {
  if (typeof ts !== "number" || !Number.isFinite(ts) || ts <= 0) {
    return "";
  }
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(ts * 1000));
}

function shortId(value: string | undefined) {
  if (!value) {
    return "";
  }
  return value.length > 12 ? value.slice(0, 12) : value;
}

function textFromMessage(event: MessageEvent | null | undefined) {
  if (!event) {
    return "";
  }
  if (typeof event.text === "string" && event.text.trim()) {
    return event.text;
  }
  if (typeof event.output === "string" && event.output.trim()) {
    return event.output;
  }
  if (typeof event.summary === "string" && event.summary.trim()) {
    return event.summary;
  }
  if (event.message?.content?.length) {
    return event.message.content
      .map((part) => typeof part.text === "string" ? part.text : "")
      .filter(Boolean)
      .join("\n");
  }
  if (typeof event.name === "string" && event.name.trim()) {
    return event.name;
  }
  return "";
}

function eventLabel(event: MessageEvent) {
  const role = typeof event.role === "string" && event.role.trim() ? event.role.trim() : "";
  const type = typeof event.type === "string" && event.type.trim() ? event.type.trim() : "";
  return role || type || "event";
}

function eventTextPreview(event: MessageEvent) {
  return textFromMessage(event) || eventLabel(event);
}

function turnHasContent(turn: CodexSessionFileTurn) {
  return Boolean(textFromMessage(turn.user) || textFromMessage(turn.assistant) || turn.messages?.length);
}

function sameEvent(left: MessageEvent, right: MessageEvent | null | undefined) {
  if (!right) {
    return false;
  }
  if (left.seq !== undefined && right.seq !== undefined) {
    return left.seq === right.seq;
  }
  if (typeof left.event_id === "string" && typeof right.event_id === "string") {
    return left.event_id === right.event_id;
  }
  return left.role === right.role && textFromMessage(left) === textFromMessage(right);
}

function SessionFileListItem({
  item,
  selected,
  onSelect,
}: {
  item: CodexSessionFileSummary;
  selected: boolean;
  onSelect(): void;
}) {
  const title = titleForSummary(item);
  const date = formatSessionDate(item.updated_ts);
  return (
    <button
      type="button"
      className={cn(
        "flex w-full flex-col gap-2 border-b border-border/60 px-3 py-3 text-left transition-colors hover:bg-muted/55",
        selected && "bg-muted",
      )}
      aria-current={selected ? "true" : undefined}
      onClick={onSelect}
    >
      <div className="flex min-w-0 items-start justify-between gap-3">
        <span className="line-clamp-2 text-sm font-medium leading-5 text-foreground">{title}</span>
        {date ? <span className="shrink-0 text-xs text-muted-foreground">{date}</span> : null}
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <Badge variant="outline" className="rounded-md px-1.5 py-0 font-normal">{item.source || "codex"}</Badge>
        {item.archived ? <Badge variant="secondary" className="rounded-md px-1.5 py-0 font-normal">archived</Badge> : null}
        <span className="truncate">{shortId(item.thread_id)}</span>
      </div>
      {item.cwd ? <div className="truncate text-xs text-muted-foreground">{item.cwd}</div> : null}
    </button>
  );
}

function SessionMessageBubble({ event, fallbackRole }: { event: MessageEvent; fallbackRole: "user" | "assistant" | "event" }) {
  const text = eventTextPreview(event);
  const role = (typeof event.role === "string" && event.role.trim()) || fallbackRole;
  return (
    <div className={cn("flex", role === "user" ? "justify-end" : "justify-start")}>
      <article
        className={cn(
          "max-w-[84%] rounded-lg border px-3 py-2 text-sm leading-6 shadow-sm",
          role === "user"
            ? "border-primary/25 bg-primary text-primary-foreground"
            : role === "assistant"
              ? "border-border bg-background"
              : "border-border/70 bg-muted/45 text-muted-foreground",
        )}
      >
        <div className="mb-1 text-[11px] font-semibold uppercase tracking-normal opacity-70">{role}</div>
        <pre className="whitespace-pre-wrap break-words font-sans">{text}</pre>
      </article>
    </div>
  );
}

function SessionTurn({ turn }: { turn: CodexSessionFileTurn }) {
  const extraEvents = (turn.messages || []).filter((event) => !sameEvent(event, turn.user) && !sameEvent(event, turn.assistant));
  return (
    <section className="flex flex-col gap-3" aria-label={`Turn ${turn.index}`}>
      {turn.user ? <SessionMessageBubble event={turn.user} fallbackRole="user" /> : null}
      {extraEvents.map((event, index) => (
        <SessionMessageBubble key={`${event.seq ?? "event"}-${index}`} event={event} fallbackRole="event" />
      ))}
      {turn.assistant ? <SessionMessageBubble event={turn.assistant} fallbackRole="assistant" /> : null}
    </section>
  );
}

export function SessionFileView({ open, activeCwd = "", onClose, onRenamed }: SessionFileViewProps) {
  const [scope, setScope] = useState<SessionFileScope>(activeCwd ? "cwd" : "all");
  const [query, setQuery] = useState("");
  const [items, setItems] = useState<CodexSessionFileSummary[]>([]);
  const [selectedThreadId, setSelectedThreadId] = useState("");
  const [detail, setDetail] = useState<CodexSessionFileResponse | null>(null);
  const [listStatus, setListStatus] = useState("");
  const [detailStatus, setDetailStatus] = useState("");
  const [renameDraft, setRenameDraft] = useState("");
  const [renameStatus, setRenameStatus] = useState("");
  const [refreshKey, setRefreshKey] = useState(0);

  useEffect(() => {
    if (!open) {
      return;
    }
    setScope(activeCwd ? "cwd" : "all");
    setQuery("");
    setSelectedThreadId("");
    setDetail(null);
    setRenameDraft("");
    setRenameStatus("");
  }, [activeCwd, open]);

  useEffect(() => {
    if (!open) {
      return undefined;
    }
    const controller = new AbortController();
    setListStatus("Loading sessions...");
    api.getCodexSessionFiles({
      scope,
      cwd: scope === "cwd" ? activeCwd : undefined,
      query,
      limit: 100,
    }, controller.signal)
      .then((response) => {
        const nextItems = response.items || [];
        setItems(nextItems);
        setListStatus("");
        setSelectedThreadId((current) => {
          if (current && nextItems.some((item) => item.thread_id === current)) {
            return current;
          }
          return nextItems[0]?.thread_id || "";
        });
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        setItems([]);
        setSelectedThreadId("");
        setListStatus(error instanceof Error ? error.message : "Unable to load sessions");
      });
    return () => {
      controller.abort();
    };
  }, [activeCwd, open, query, refreshKey, scope]);

  useEffect(() => {
    if (!open || !selectedThreadId) {
      setDetail(null);
      return undefined;
    }
    const controller = new AbortController();
    setDetailStatus("Loading transcript...");
    api.getCodexSessionFile(selectedThreadId, { limit: 500 }, controller.signal)
      .then((response) => {
        setDetail(response);
        setDetailStatus("");
        setRenameDraft(titleForSummary(response.summary));
        setRenameStatus("");
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        setDetail(null);
        setDetailStatus(error instanceof Error ? error.message : "Unable to load transcript");
      });
    return () => {
      controller.abort();
    };
  }, [open, selectedThreadId, refreshKey]);

  const selectedSummary = useMemo(() => {
    return detail?.summary || items.find((item) => item.thread_id === selectedThreadId) || null;
  }, [detail?.summary, items, selectedThreadId]);

  const visibleTurns = useMemo(() => {
    return (detail?.turns || []).filter(turnHasContent);
  }, [detail?.turns]);

  const canUseCwdScope = Boolean(activeCwd.trim());
  const renameChanged = renameDraft.trim() && renameDraft.trim() !== titleForSummary(selectedSummary);

  const renameSelected = async () => {
    const threadId = selectedSummary?.thread_id || selectedThreadId;
    const name = renameDraft.trim();
    if (!threadId || !name) {
      return;
    }
    setRenameStatus("Renaming...");
    try {
      const response = await api.renameCodexSessionFile(threadId, name);
      setDetail((current) => ({
        ...(current || {}),
        summary: response.summary || current?.summary || selectedSummary || undefined,
      }));
      setItems((current) => current.map((item) => item.thread_id === threadId
        ? { ...item, ...(response.summary || {}), title: response.summary?.title || name, display_name: response.summary?.display_name || name }
        : item));
      setRenameStatus("Renamed");
      setRefreshKey((current) => current + 1);
      onRenamed?.();
    } catch (error) {
      setRenameStatus(error instanceof Error ? error.message : "Unable to rename session");
    }
  };

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (!nextOpen) {
        onClose();
      }
    }}>
      <DialogContent className="mobileDetailDialog max-w-[1180px]" titleId="session-file-view-title">
        <DialogHeader>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="space-y-1">
              <DialogTitle id="session-file-view-title">Codex Sessions</DialogTitle>
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span>{items.length} files</span>
                {scope === "cwd" && activeCwd ? <span className="max-w-[52rem] truncate">{activeCwd}</span> : null}
              </div>
            </div>
            <Button type="button" variant="ghost" size="sm" onClick={onClose}>Close</Button>
          </div>
        </DialogHeader>

        <div className="grid min-h-0 flex-1 gap-0 overflow-hidden p-5 pt-4 lg:grid-cols-[360px_minmax(0,1fr)]">
          <aside className="flex min-h-[260px] flex-col overflow-hidden border border-border bg-card lg:min-h-0">
            <div className="border-b border-border p-3">
              <div className="inline-flex h-9 rounded-lg bg-secondary p-1 text-sm text-secondary-foreground" role="group" aria-label="Session file scope">
                <button
                  type="button"
                  className={cn("rounded-md px-3 font-medium", scope === "cwd" && "bg-background shadow-sm")}
                  disabled={!canUseCwdScope}
                  onClick={() => setScope("cwd")}
                >
                  cwd
                </button>
                <button
                  type="button"
                  className={cn("rounded-md px-3 font-medium", scope === "all" && "bg-background shadow-sm")}
                  onClick={() => setScope("all")}
                >
                  all
                </button>
              </div>
              <Input
                className="mt-3"
                value={query}
                placeholder="Filter sessions"
                aria-label="Filter sessions"
                onInput={(event) => setQuery(event.currentTarget.value)}
              />
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto">
              {items.length ? items.map((item) => (
                <SessionFileListItem
                  key={item.thread_id || item.path}
                  item={item}
                  selected={item.thread_id === selectedThreadId}
                  onSelect={() => setSelectedThreadId(item.thread_id)}
                />
              )) : (
                <div className="p-4 text-sm text-muted-foreground">{listStatus || "No sessions."}</div>
              )}
            </div>
            {listStatus && items.length ? <div className="border-t border-border px-3 py-2 text-xs text-muted-foreground">{listStatus}</div> : null}
          </aside>

          <section className="flex min-h-[420px] flex-col overflow-hidden border border-l-0 border-border bg-muted/15 lg:min-h-0">
            {selectedSummary ? (
              <>
                <header className="border-b border-border bg-card p-4">
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="min-w-0">
                      <h3 className="truncate text-base font-semibold">{titleForSummary(selectedSummary)}</h3>
                      <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                        <span>{shortId(selectedSummary.thread_id)}</span>
                        {selectedSummary.path ? <span className="max-w-[48rem] truncate">{selectedSummary.path}</span> : null}
                      </div>
                    </div>
                    <div className="flex min-w-[260px] max-w-md flex-1 items-center gap-2">
                      <Input
                        value={renameDraft}
                        aria-label="Session name"
                        onInput={(event) => setRenameDraft(event.currentTarget.value)}
                        onKeyDown={(event) => {
                          if (event.key === "Enter" && renameChanged) {
                            void renameSelected();
                          }
                        }}
                      />
                      <Button type="button" size="sm" disabled={!renameChanged} onClick={() => { void renameSelected(); }}>
                        Rename
                      </Button>
                    </div>
                  </div>
                  {renameStatus ? <div className="mt-2 text-xs text-muted-foreground">{renameStatus}</div> : null}
                </header>
                <div className="min-h-0 flex-1 overflow-y-auto p-4">
                  {detailStatus ? <div className="mb-3 text-sm text-muted-foreground">{detailStatus}</div> : null}
                  {visibleTurns.length ? (
                    <div className="mx-auto flex max-w-4xl flex-col gap-5">
                      {visibleTurns.map((turn) => <SessionTurn key={turn.index} turn={turn} />)}
                    </div>
                  ) : detail?.items?.length ? (
                    <div className="mx-auto flex max-w-4xl flex-col gap-3">
                      {detail.items.map((event, index) => (
                        <SessionMessageBubble key={`${event.seq ?? "event"}-${index}`} event={event} fallbackRole="event" />
                      ))}
                    </div>
                  ) : !detailStatus ? (
                    <div className="text-sm text-muted-foreground">No transcript messages.</div>
                  ) : null}
                </div>
              </>
            ) : (
              <div className="p-5 text-sm text-muted-foreground">{listStatus || "Select a session."}</div>
            )}
          </section>
        </div>
      </DialogContent>
    </Dialog>
  );
}
