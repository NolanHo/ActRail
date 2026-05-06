import { useMemo, useState } from "preact/hooks";

import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

import { api } from "../../lib/api";
import type { MessageEvent } from "../../lib/types";
import { buildTrace, type TraceNode } from "../../domains/trace/model";

interface TraceViewProps {
  sessionId: string | null;
  runtimeId?: string | null;
  messages: MessageEvent[];
}

function formatDuration(seconds: number | undefined): string {
  if (seconds === undefined) {
    return "";
  }
  if (seconds < 1) {
    return `${Math.round(seconds * 1000)}ms`;
  }
  if (seconds < 60) {
    return `${seconds.toFixed(seconds < 10 ? 1 : 0)}s`;
  }
  return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`;
}

function traceDetailKey(node: TraceNode): string {
  return node.toolCallId ? `tool:${node.toolCallId}` : node.eventId ? `event:${node.eventId}` : node.id;
}

function stringifyDetail(value: unknown): string {
  if (value === undefined) {
    return "";
  }
  if (typeof value === "string") {
    return value;
  }
  return JSON.stringify(value, null, 2);
}

function nodeRawEvents(node: TraceNode, hydrated: { call?: MessageEvent; result?: MessageEvent; event?: MessageEvent }): Record<string, unknown> {
  return {
    node: {
      id: node.id,
      kind: node.kind,
      label: node.label,
      status: node.status,
      edgeConfidence: node.edgeConfidence,
      seq: node.seq,
      ts: node.ts,
      eventId: node.eventId,
      parentEventId: node.parentEventId,
      toolCallId: node.toolCallId,
      durationSeconds: node.durationSeconds,
    },
    call: hydrated.call ?? node.call,
    result: hydrated.result ?? node.result,
    event: hydrated.event ?? node.event,
  };
}

function statusLabel(node: TraceNode): string {
  if (node.kind !== "tool") {
    return node.status || "event";
  }
  return node.status || "running";
}

function TraceNodeRow({ node, depth, sessionId, runtimeId }: { node: TraceNode; depth: number; sessionId: string; runtimeId?: string | null }) {
  const [expanded, setExpanded] = useState(false);
  const [hydrated, setHydrated] = useState<{ call?: MessageEvent; result?: MessageEvent; event?: MessageEvent }>({});
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [detailError, setDetailError] = useState("");
  const canHydrate = Boolean(node.eventId || node.toolCallId);
  const detailKey = traceDetailKey(node);
  const duration = formatDuration(node.durationSeconds);

  const hydrate = async () => {
    if (!canHydrate || loadingDetail || hydrated.call || hydrated.result || hydrated.event) {
      return;
    }
    setLoadingDetail(true);
    setDetailError("");
    const loadOne = async (eventId: string, toolCallId: string): Promise<MessageEvent | undefined> => {
      const response = await api.listMessages(
        sessionId,
        false,
        undefined,
        undefined,
        undefined,
        undefined,
        runtimeId,
        false,
        undefined,
        0,
        true,
        eventId,
        toolCallId,
      );
      const items = Array.isArray(response.items) ? response.items : Array.isArray(response.events) ? response.events : [];
      return items[0];
    };
    try {
      if (node.kind === "tool") {
        const [call, result] = await Promise.all([
          node.call?.event_id ? loadOne(node.call.event_id, "") : undefined,
          node.result?.event_id ? loadOne(node.result.event_id, "") : undefined,
        ]);
        if (call || result) {
          setHydrated({ call, result });
        } else if (node.toolCallId) {
          const item = await loadOne("", node.toolCallId);
          setHydrated(item?.type === "tool_result" ? { result: item } : { call: item });
        }
      } else if (node.eventId) {
        setHydrated({ event: await loadOne(node.eventId, "") });
      }
    } catch (error) {
      setDetailError(error instanceof Error ? error.message : String(error));
    } finally {
      setLoadingDetail(false);
    }
  };

  const toggleExpanded = () => {
    const next = !expanded;
    setExpanded(next);
    if (next) {
      void hydrate();
    }
  };

  return (
    <div className="traceNode" style={{ marginLeft: `${depth * 1.1}rem` }}>
      <button
        type="button"
        className="traceNodeButton w-full rounded-2xl border border-border/60 bg-background/80 px-3 py-2 text-left transition hover:bg-accent/40"
        onClick={toggleExpanded}
        aria-expanded={expanded}
      >
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span className="rounded-full border border-border/60 px-2 py-0.5 uppercase tracking-wide">{node.kind}</span>
          <span className={cn("rounded-full px-2 py-0.5", node.edgeConfidence === "explicit" ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground")}>{node.edgeConfidence}</span>
          <span className="rounded-full bg-muted px-2 py-0.5">{statusLabel(node)}</span>
          {duration ? <span>{duration}</span> : null}
          {node.seq !== undefined ? <span>seq {node.seq}</span> : null}
        </div>
        <div className="mt-1 font-medium text-foreground">{node.label}</div>
        {node.summary ? <div className="mt-1 line-clamp-2 text-sm text-muted-foreground">{node.summary}</div> : null}
      </button>
      {expanded ? (
        <div className="mt-2 rounded-2xl border border-border/50 bg-muted/20 p-3 text-xs" data-testid={`trace-detail-${detailKey}`}>
          <div className="mb-2 flex flex-wrap gap-2 text-muted-foreground">
            {node.eventId ? <span>event_id: {node.eventId}</span> : null}
            {node.parentEventId ? <span>parent_event_id: {node.parentEventId}</span> : null}
            {node.toolCallId ? <span>tool_call_id: {node.toolCallId}</span> : null}
          </div>
          {loadingDetail ? <div className="text-muted-foreground">Loading detail...</div> : null}
          {detailError ? <div className="text-destructive">{detailError}</div> : null}
          <pre className="max-h-80 overflow-auto whitespace-pre-wrap rounded-xl bg-background/80 p-3 text-[11px] leading-relaxed text-foreground">
            {stringifyDetail(nodeRawEvents(node, hydrated))}
          </pre>
        </div>
      ) : null}
      {node.children.length ? (
        <div className="mt-2 flex flex-col gap-2">
          {node.children.map((child) => <TraceNodeRow key={child.id} node={child} depth={depth + 1} sessionId={sessionId} runtimeId={runtimeId} />)}
        </div>
      ) : null}
    </div>
  );
}

export function TraceView({ sessionId, runtimeId, messages }: TraceViewProps) {
  const trace = useMemo(() => buildTrace(messages), [messages]);

  if (!sessionId) {
    return (
      <Card className="rounded-[1.35rem] border-dashed border-border/60 bg-muted/20 shadow-none">
        <CardContent className="p-6 text-sm text-muted-foreground">Select a session to see its trace.</CardContent>
      </Card>
    );
  }

  if (!trace.length) {
    return (
      <Card className="rounded-[1.35rem] border-dashed border-border/60 bg-muted/20 shadow-none">
        <CardContent className="p-6 text-sm text-muted-foreground">No traceable events in the loaded conversation page.</CardContent>
      </Card>
    );
  }

  return (
    <div className="traceView flex flex-col gap-3" data-testid="trace-view">
      <div className="rounded-2xl border border-border/60 bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
        Trace uses loaded conversation events. Explicit edges come from runtime parent_event_id. Inferred edges are display-only.
      </div>
      {trace.map((node) => <TraceNodeRow key={node.id} node={node} depth={0} sessionId={sessionId} runtimeId={runtimeId} />)}
    </div>
  );
}
