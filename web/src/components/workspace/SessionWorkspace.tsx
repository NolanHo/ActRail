import type { ComponentChildren, JSX } from "preact";
import { useRef, useState } from "preact/hooks";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";

import { useLiveSessionStore, useLiveSessionStoreApi, useSessionUiStore, useSessionUiStoreApi, useSessionsStore, useWaitsStore } from "../../app/providers";
import { api } from "../../lib/api";
import { calculateContextTokenUsage, type ContextTokenUsageResult } from "../../lib/context-token-usage";
import { getSessionDisplayName } from "../../lib/session-display";
import type { MessageEvent, SessionSummary, SessionUiRequest } from "../../lib/types";
import type { AskUserBridgeAnswers } from "../../domains/ask-user/contract";
import { encodeAskUserBridgeResponse, parseAskUserBridgeRequest } from "../../domains/ask-user/codec";
import { getInitialDraftValue, normalizeOption, normalizeRequestValue } from "../../domains/ask-user/normalize";
import { WaitInbox } from "../waits/WaitInbox";
import { WaitThreadPanel } from "../waits/WaitThreadPanel";

type DraftValue = string | string[];

function getRequestHeading(request: SessionUiRequest): string {
  return request.title || request.label || request.question || request.method || "Request";
}

function getRequestBody(request: SessionUiRequest): string {
  return request.message || request.context || "";
}

function entriesFromRecord(value: Record<string, unknown> | null) {
  return value ? Object.entries(value) : [];
}

function queueItemsFromValue(queue: Record<string, unknown> | null) {
  const rawItems = queue?.items;
  if (!Array.isArray(rawItems)) {
    return [] as Array<{ id: string; text: string }>;
  }
  return rawItems.map((item, index) => {
    if (item && typeof item === "object") {
      const record = item as { id?: unknown; queue_id?: unknown; text?: unknown };
      return {
        id: String(record.id ?? record.queue_id ?? index),
        text: String(record.text ?? ""),
      };
    }
    return { id: String(index), text: String(item) };
  }).filter((item) => item.text.trim().length > 0);
}

function formatDiagnosticLabel(key: string): string {
  switch (key) {
    case "log_path":
      return "Log";
    case "session_file_path":
      return "Session file";
    case "updated_ts":
      return "Updated";
    case "cwd":
      return "Working directory";
    case "queue_len":
      return "Queue";
    default:
      return key.replace(/_/g, " ").replace(/\b\w/g, (char) => char.toUpperCase());
  }
}

function formatDiagnosticValue(key: string, value: unknown): string {
  if (key === "updated_ts" && typeof value === "number" && Number.isFinite(value) && value > 1_000_000_000) {
    return new Date(value * 1000).toLocaleString();
  }
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return JSON.stringify(value);
}

function metadataEntriesFromSession(session: SessionSummary | null, sessionId: string | null, runtimeId: string | null) {
  const entries: Array<[string, unknown]> = [];
  const push = (key: string, value: unknown) => {
    if (value == null) {
      return;
    }
    if (typeof value === "string" && value.trim().length === 0) {
      return;
    }
    entries.push([key, value]);
  };

  if (session) {
    push("display_name", getSessionDisplayName(session));
  }
  push("session_id", session?.session_id ?? sessionId);
  push("session_file_path", session?.session_file_path);
  push("backend_session_id", session?.backend_session_id);
  push("runtime_id", session?.runtime_id ?? runtimeId);
  push("agent_backend", session?.agent_backend);
  push("transport", session?.transport);
  push("cwd", session?.cwd);
  push("git_branch", session?.git_branch);
  push("model", session?.model);
  push("reasoning_effort", session?.reasoning_effort);
  push("service_tier", session?.service_tier);
  push("busy", session?.busy);
  push("focused", session?.focused);
  push("queue_len", session?.queue_len);
  push("historical", session?.historical);
  return entries;
}

function isMonospaceMetadataKey(key: string) {
  return key.endsWith("_id") || key.endsWith("_path") || key === "cwd" || key === "git_branch";
}

function renderKeyValueList(entries: Array<[string, unknown]>) {
  return (
    <dl className="space-y-3">
      {entries.map(([key, value]) => (
        <div key={key} className="grid gap-1 sm:grid-cols-[minmax(7rem,auto)_1fr] sm:gap-3">
          <dt className="text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground">{formatDiagnosticLabel(key)}</dt>
          <dd className={`m-0 text-sm text-foreground${isMonospaceMetadataKey(key) ? " break-all font-mono" : ""}`}>{formatDiagnosticValue(key, value)}</dd>
        </div>
      ))}
    </dl>
  );
}

function mergeFreeformValue(request: SessionUiRequest, normalizedValue: string | string[] | undefined, freeformValue: string) {
  const trimmedFreeform = freeformValue.trim();

  if (!trimmedFreeform) {
    return normalizedValue;
  }

  if (request.allow_multiple) {
    const existingValues = Array.isArray(normalizedValue)
      ? normalizedValue
      : normalizedValue
        ? [normalizedValue]
        : [];
    return [...existingValues, trimmedFreeform];
  }

  return trimmedFreeform;
}

function SelectField(props: JSX.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
      {...props}
    />
  );
}

function WorkspaceSection({
  title,
  badge,
  children,
}: {
  title: string;
  badge?: string;
  children: ComponentChildren;
}) {
  return (
    <section className="workspaceSurface space-y-3 rounded-[1.2rem] border border-border/70 bg-background/75 p-4 shadow-sm">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold text-foreground">{title}</h3>
        {badge ? <Badge variant="outline">{badge}</Badge> : null}
      </div>
      {children}
    </section>
  );
}

function formatTokenCount(value: number) {
  return String(Math.max(0, Math.round(value)));
}

function contextUsageAnchor(usage: ContextTokenUsageResult) {
  return `system prompt: ${formatTokenCount(usage.buckets.systemPrompt.tokens)} tool: ${formatTokenCount(usage.buckets.tool.tokens)}, user: ${formatTokenCount(usage.buckets.user.tokens)}, assist: ${formatTokenCount(usage.buckets.assist.tokens)}`;
}

function contextUsagePieStyle(usage: ContextTokenUsageResult) {
  const system = usage.buckets.systemPrompt.percent;
  const tool = system + usage.buckets.tool.percent;
  const user = tool + usage.buckets.user.percent;
  return {
    background: `conic-gradient(var(--context-system) 0 ${system}%, var(--context-tool) ${system}% ${tool}%, var(--context-user) ${tool}% ${user}%, var(--context-assist) ${user}% 100%)`,
  } as JSX.CSSProperties;
}

function ContextUsagePanel({
  calculating,
  error,
  usage,
  onCalculate,
}: {
  calculating: boolean;
  error: string;
  usage: ContextTokenUsageResult | null;
  onCalculate(): void;
}) {
  const buckets: Array<[keyof ContextTokenUsageResult["buckets"], string]> = [
    ["systemPrompt", "system prompt"],
    ["tool", "tool"],
    ["user", "user"],
    ["assist", "assist"],
  ];
  return (
    <WorkspaceSection title="Context Usage" badge={usage ? `${formatTokenCount(usage.totalTokens)} tokens` : undefined}>
      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" size="sm" disabled={calculating} onClick={onCalculate}>
            {calculating ? "calculating" : "Calculate"}
          </Button>
          {usage?.fallback ? <Badge variant="outline">fallback: chars/4</Badge> : null}
        </div>
        {usage ? (
          <div className="space-y-4">
            <p className="contextUsageAnchor font-mono text-sm text-foreground">{contextUsageAnchor(usage)}</p>
            <div className="contextUsageChartWrap">
              <div className="contextUsagePie" style={contextUsagePieStyle(usage)} aria-hidden="true" />
              <dl className="contextUsageLegend">
                {buckets.map(([key, label]) => (
                  <div key={key} className="contextUsageLegendItem">
                    <dt><span className={`contextUsageSwatch is-${key}`} />{label}</dt>
                    <dd>{formatTokenCount(usage.buckets[key].tokens)} tokens, {usage.buckets[key].percent}%</dd>
                  </div>
                ))}
              </dl>
            </div>
            <p className="text-xs text-muted-foreground">
              {usage.fallback ? usage.fallbackReason : `tokenizer: ${usage.model}`}
            </p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">Manual calculation is required.</p>
        )}
        {error ? <p className="text-sm font-medium text-destructive">{error}</p> : null}
      </div>
    </WorkspaceSection>
  );
}

interface SessionWorkspaceProps {
  mode?: "default" | "details";
  initialTab?: "insight" | "overview" | "wait" | "waiting-inbox" | "requests" | "metadata" | "diagnostics" | "queue" | "files" | "supervisor";
}

export function SessionWorkspace({ mode = "default", initialTab }: SessionWorkspaceProps) {
  const sessionUiState = useSessionUiStore() as {
    sessionId: string | null;
    runtimeId: string | null;
    diagnostics: Record<string, unknown> | null;
    queue: Record<string, unknown> | null;
    loading: boolean;
    requests?: SessionUiRequest[];
    files?: string[];
  };
  const { sessionId, runtimeId, diagnostics, queue, loading } = sessionUiState;
  const { items, activeSessionId } = useSessionsStore();
  const workspaceSessionId = sessionId ?? activeSessionId;
  const liveSessionState = useLiveSessionStore();
  const waitsState = useWaitsStore();
  const liveSessionStoreApi = useLiveSessionStoreApi();
  const liveRequests = sessionId ? liveSessionState.requestsBySessionId[sessionId] ?? [] : [];
  const requests = liveRequests.length ? liveRequests : Array.isArray(sessionUiState.requests) ? sessionUiState.requests : [];
  const files = Array.isArray(sessionUiState.files) ? sessionUiState.files : [];
  const sessionUiStoreApi = useSessionUiStoreApi();
  const [drafts, setDrafts] = useState<Record<string, DraftValue>>({});
  const [freeformDrafts, setFreeformDrafts] = useState<Record<string, string>>({});
  const [askUserBridgeDrafts, setAskUserBridgeDrafts] = useState<Record<string, AskUserBridgeAnswers>>({});
  const [requestSubmittingById, setRequestSubmittingById] = useState<Record<string, boolean>>({});
  const [requestErrorById, setRequestErrorById] = useState<Record<string, string>>({});
  const [queueCancelling, setQueueCancelling] = useState(false);
  const [contextUsageResult, setContextUsageResult] = useState<ContextTokenUsageResult | null>(null);
  const [contextUsageCalculating, setContextUsageCalculating] = useState(false);
  const [contextUsageError, setContextUsageError] = useState("");
  const requestSubmittingIdsRef = useRef(new Set<string>());
  const diagnosticsEntries = entriesFromRecord(diagnostics);
  const activeSession = workspaceSessionId ? items.find((item) => item.session_id === workspaceSessionId) ?? null : null;
  const activeWait = workspaceSessionId ? waitsState.activeBySessionId[workspaceSessionId] ?? activeSession?.active_wait ?? null : null;
  const metadataEntries = metadataEntriesFromSession(activeSession, workspaceSessionId, runtimeId);
  const summarySessionFilePath = typeof activeSession?.session_file_path === "string" && activeSession.session_file_path.trim().length > 0;
  const detailEntries = diagnosticsEntries.filter(([key]) => key !== "todo_snapshot" && !(key === "session_file_path" && summarySessionFilePath));
  const prioritizedDetailKeys = new Set(["session_file_path", "log_path", "updated_ts"]);
  const priorityDetailEntries = detailEntries.filter(([key]) => prioritizedDetailKeys.has(key));
  const genericDetailEntries = detailEntries.filter(([key]) => !prioritizedDetailKeys.has(key));
  const queueItems = queueItemsFromValue(queue);
  const showDetails = mode === "details";
  const hasWorkspaceData = metadataEntries.length > 0 || detailEntries.length > 0 || queueItems.length > 0 || Boolean(activeWait);
  const showTabs = true;
  const derivedDefaultTab = activeWait
    ? "wait"
    : showDetails
      ? "overview"
      : requests.length > 0
      ? "requests"
      : metadataEntries.length > 0
        ? "metadata"
        : detailEntries.length > 0
          ? "diagnostics"
          : queueItems.length > 0
            ? "queue"
            : "insight";
  const defaultTab = initialTab ?? derivedDefaultTab;

  const calculateContextUsage = async () => {
    if (!workspaceSessionId || contextUsageCalculating) {
      return;
    }
    setContextUsageCalculating(true);
    setContextUsageError("");
    try {
      const payload = runtimeId
        ? await api.listMessages(workspaceSessionId, true, undefined, undefined, undefined, undefined, runtimeId)
        : await api.listMessages(workspaceSessionId, true);
      const events = Array.isArray(payload.items)
        ? payload.items
        : Array.isArray(payload.events)
          ? payload.events
          : [];
      const diagnosticsModel = diagnostics && typeof diagnostics === "object" && typeof (diagnostics as { model?: unknown }).model === "string"
        ? (diagnostics as { model: string }).model
        : "";
      setContextUsageResult(await calculateContextTokenUsage(events as MessageEvent[], activeSession?.model || diagnosticsModel));
    } catch (error) {
      setContextUsageError(error instanceof Error && error.message.trim() ? error.message : "Context usage calculation failed");
    } finally {
      setContextUsageCalculating(false);
    }
  };

  const cancelQueue = async () => {
    if (!sessionId || queueCancelling) {
      return;
    }
    setQueueCancelling(true);
    try {
      await (runtimeId ? api.cancelQueue(sessionId, runtimeId) : api.cancelQueue(sessionId));
      await Promise.allSettled([
        runtimeId ? liveSessionStoreApi.loadInitial(sessionId, runtimeId) : liveSessionStoreApi.loadInitial(sessionId),
        runtimeId ? sessionUiStoreApi.refresh(sessionId, { agentBackend: activeSession?.agent_backend, runtimeId }) : sessionUiStoreApi.refresh(sessionId, { agentBackend: activeSession?.agent_backend }),
      ]);
    } finally {
      setQueueCancelling(false);
    }
  };

  const submitRequestResponse = async (requestId: string, payload: Record<string, unknown>) => {
    if (!sessionId || requestSubmittingIdsRef.current.has(requestId)) {
      return;
    }

    requestSubmittingIdsRef.current.add(requestId);
    setRequestSubmittingById((current) => ({ ...current, [requestId]: true }));
    setRequestErrorById((current) => ({ ...current, [requestId]: "" }));

    try {
      await (runtimeId ? api.submitUiResponse(sessionId, payload, runtimeId) : api.submitUiResponse(sessionId, payload));
      await Promise.all([
        runtimeId ? liveSessionStoreApi.loadInitial(sessionId, runtimeId) : liveSessionStoreApi.loadInitial(sessionId),
        runtimeId ? sessionUiStoreApi.refresh(sessionId, { agentBackend: "pi", runtimeId }) : sessionUiStoreApi.refresh(sessionId, { agentBackend: "pi" }),
      ]);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Failed to submit response";
      setRequestErrorById((current) => ({ ...current, [requestId]: message }));
    } finally {
      requestSubmittingIdsRef.current.delete(requestId);
      setRequestSubmittingById((current) => ({ ...current, [requestId]: false }));
    }
  };

  return (
    <aside className="workspacePane">
      <Card
        data-testid="workspace-card"
        className="workspaceCard flex h-full min-h-0 flex-col rounded-[1.5rem] border-border/70 bg-card/95 shadow-lg shadow-primary/5"
      >
        <CardHeader className="space-y-4 pb-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="space-y-1">
              <CardTitle className="text-base">Details</CardTitle>
              <p className="text-sm text-muted-foreground">
                {requests.length ? `${requests.length} pending UI request${requests.length === 1 ? "" : "s"}` : "No pending UI requests"}
              </p>
            </div>
            <Badge variant={loading ? "default" : hasWorkspaceData ? "secondary" : "outline"}>
              {loading ? "Refreshing" : hasWorkspaceData ? "Live context" : "Quiet"}
            </Badge>
          </div>
          {showTabs ? (
            <Tabs defaultValue={defaultTab} className="min-h-0 flex-1">
              <TabsList className="workspaceTabsList flex h-auto flex-wrap items-center gap-2 rounded-2xl bg-muted/60 p-1">
                <TabsTrigger value="insight">Insight</TabsTrigger>
                {showDetails ? <TabsTrigger value="overview">Overview</TabsTrigger> : null}
                <TabsTrigger value="wait">Wait</TabsTrigger>
                <TabsTrigger value="waiting-inbox">Waiting Inbox</TabsTrigger>
                <TabsTrigger value="requests">UI Requests</TabsTrigger>
                <TabsTrigger value="metadata">Metadata</TabsTrigger>
                <TabsTrigger value="supervisor">Supervisor</TabsTrigger>
                <TabsTrigger value="diagnostics">Diagnostics</TabsTrigger>
                <TabsTrigger value="queue">Queue</TabsTrigger>
                {files.length ? <TabsTrigger value="files">Files</TabsTrigger> : null}
              </TabsList>
              <Separator className="bg-border/70" />
              <CardContent className="flex min-h-0 flex-1 flex-col p-0 pt-4">
                <TabsContent value="insight" className="min-h-0">
                  <ScrollArea className="workspaceScroll h-full pr-1">
                    <Tabs defaultValue="context-usage" className="min-h-0">
                      <TabsList className="workspaceTabsList flex h-auto flex-wrap items-center gap-2 rounded-2xl bg-muted/60 p-1">
                        <TabsTrigger value="context-usage">Context Usage</TabsTrigger>
                      </TabsList>
                      <TabsContent value="context-usage" className="min-h-0">
                        <ContextUsagePanel
                          calculating={contextUsageCalculating}
                          error={contextUsageError}
                          usage={contextUsageResult}
                          onCalculate={() => { void calculateContextUsage(); }}
                        />
                      </TabsContent>
                    </Tabs>
                  </ScrollArea>
                </TabsContent>
                {showDetails ? (
                  <TabsContent value="overview" className="min-h-0">
                    <ScrollArea className="workspaceScroll h-full pr-1">
                      <div className="workspacePanelGrid grid gap-4 lg:grid-cols-2">
                        <WorkspaceSection title="Metadata" badge={metadataEntries.length ? `${metadataEntries.length}` : undefined}>
                          {metadataEntries.length ? renderKeyValueList(metadataEntries) : <p className="text-sm text-muted-foreground">No metadata available.</p>}
                        </WorkspaceSection>
                        <WorkspaceSection title="Diagnostics" badge={detailEntries.length ? `${detailEntries.length}` : undefined}>
                          {detailEntries.length ? (
                            <div className="space-y-4">
                              {priorityDetailEntries.length ? renderKeyValueList(priorityDetailEntries) : null}
                              {genericDetailEntries.length ? renderKeyValueList(genericDetailEntries) : null}
                            </div>
                          ) : (
                            <p className="text-sm text-muted-foreground">No diagnostics available.</p>
                          )}
                        </WorkspaceSection>
                        <WorkspaceSection title="Queue" badge={queueItems.length ? `${queueItems.length}` : undefined}>
                          {queueItems.length ? (
                            <div className="space-y-3">
                              <ul className="workspaceCollection space-y-2 text-sm text-foreground">
                                {queueItems.map((item) => (
                                  <li key={item.id} className="rounded-xl border border-border/60 bg-card/60 px-3 py-2">
                                    {item.text}
                                  </li>
                                ))}
                              </ul>
                              <Button type="button" variant="outline" size="sm" disabled={queueCancelling} onClick={() => { void cancelQueue(); }}>
                                {queueCancelling ? "Cancelling..." : "Cancel queued send"}
                              </Button>
                            </div>
                          ) : (
                            <p className="text-sm text-muted-foreground">No queued items.</p>
                          )}
                        </WorkspaceSection>
                        {files.length ? (
                          <WorkspaceSection title="Files" badge={`${files.length}`}>
                            <ul className="workspaceCollection space-y-2 text-sm text-foreground">
                              {files.map((file) => (
                                <li key={file} className="rounded-xl border border-border/60 bg-card/60 px-3 py-2 font-mono text-xs sm:text-sm">
                                  {file}
                                </li>
                              ))}
                            </ul>
                          </WorkspaceSection>
                        ) : null}
                        <WorkspaceSection title="UI Requests" badge={requests.length ? `${requests.length}` : undefined}>
                          <p className="text-sm text-muted-foreground">
                            {requests.length ? "Review and respond in the dedicated tab." : "No pending requests."}
                          </p>
                        </WorkspaceSection>
                      </div>
                    </ScrollArea>
                  </TabsContent>
                ) : null}
                <TabsContent value="wait" className="min-h-0">
                  <WaitThreadPanel sessionId={workspaceSessionId} runtimeId={runtimeId} activeWait={activeWait} />
                </TabsContent>
                <TabsContent value="waiting-inbox" className="min-h-0">
                  <WaitInbox />
                </TabsContent>
                <TabsContent value="requests" className="min-h-0">
                  <ScrollArea className="workspaceScroll h-full pr-1">
                    <div className="space-y-4">
                      {requests.length ? (
                        requests.map((request, index) => {
                          const requestId = String(request.id ?? index);
                          const askUserBridge = parseAskUserBridgeRequest(request);
                          const draftValue = drafts[requestId] ?? getInitialDraftValue(request);
                          const freeformValue = freeformDrafts[requestId] ?? "";
                          const options = Array.isArray(request.options) ? request.options : [];
                          const bodyText = getRequestBody(request);
                          const selectedValues = Array.isArray(draftValue) ? draftValue : [];
                          const askUserBridgeAnswers = askUserBridgeDrafts[requestId] ?? {};
                          const askUserBridgeReady = Boolean(
                            askUserBridge && askUserBridge.questions.every((question) => {
                              const answer = askUserBridgeAnswers[question.question];
                              return Array.isArray(answer) ? answer.length > 0 : typeof answer === "string" && answer.trim().length > 0;
                            })
                          );

                          return (
                            <Card key={requestId} className="rounded-[1.2rem] border-border/70 bg-background/75 shadow-sm">
                              <CardContent className="space-y-4 p-4">
                                <div className="space-y-2">
                                  <div className="flex flex-wrap items-center gap-2">
                                    <Badge variant="secondary">{request.method || "request"}</Badge>
                                    {!askUserBridge && request.allow_multiple ? <Badge variant="outline">multi-select</Badge> : null}
                                    {!askUserBridge && request.allow_freeform ? <Badge variant="outline">freeform</Badge> : null}
                                  </div>
                                  <div>
                                    <h3 className="text-sm font-semibold text-foreground">{askUserBridge ? "AskUserQuestion" : getRequestHeading(request)}</h3>
                                    {bodyText ? <p className="mt-1 text-sm text-muted-foreground">{bodyText}</p> : null}
                                  </div>
                                </div>

                                {askUserBridge ? (
                                  <div className="space-y-4">
                                    {askUserBridge.questions.map((question) => {
                                      const currentAnswer = askUserBridgeAnswers[question.question];
                                      const selectedAnswer = Array.isArray(currentAnswer)
                                        ? currentAnswer
                                        : typeof currentAnswer === "string"
                                          ? [currentAnswer]
                                          : [];
                                      return (
                                        <section key={question.question} className="space-y-3 rounded-2xl border border-border/60 bg-card/60 p-3">
                                          <div>
                                            <p className="text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground">{question.header}</p>
                                            <h4 className="text-sm font-semibold text-foreground">{question.question}</h4>
                                          </div>
                                          <div className="flex flex-wrap gap-2">
                                            {question.options.map((option) => {
                                              const isSelected = selectedAnswer.includes(option.label);
                                              return (
                                                <Button
                                                  key={`${question.question}-${option.label}`}
                                                  type="button"
                                                  variant={isSelected ? "default" : "outline"}
                                                  className="h-auto min-h-10 rounded-full px-4 py-2 text-left"
                                                  onClick={() => {
                                                    setAskUserBridgeDrafts((current) => {
                                                      const existing = current[requestId] ?? {};
                                                      const previous = existing[question.question];
                                                      const previousValues = Array.isArray(previous)
                                                        ? previous
                                                        : typeof previous === "string"
                                                          ? [previous]
                                                          : [];
                                                      const nextValue = question.multiSelect
                                                        ? previousValues.includes(option.label)
                                                          ? previousValues.filter((value) => value !== option.label)
                                                          : [...previousValues, option.label]
                                                        : option.label;
                                                      return {
                                                        ...current,
                                                        [requestId]: {
                                                          ...existing,
                                                          [question.question]: nextValue,
                                                        },
                                                      };
                                                    });
                                                  }}
                                                >
                                                  <span className="flex flex-col items-start gap-1">
                                                    <span>{option.label}</span>
                                                    {option.description ? <span className="text-xs font-normal text-muted-foreground">{option.description}</span> : null}
                                                  </span>
                                                </Button>
                                              );
                                            })}
                                          </div>
                                        </section>
                                      );
                                    })}
                                  </div>
                                ) : null}

                                {!askUserBridge && (request.method === "confirm" ? null : options.length ? (
                                  request.allow_multiple ? (
                                    <div className="space-y-2">
                                      {options.map((option, optionIndex) => {
                                        const normalized = normalizeOption(option, optionIndex);
                                        return (
                                          <label key={normalized.key} className="workspaceOption flex cursor-pointer gap-3 rounded-2xl border border-border/60 bg-card/60 px-3 py-3 text-sm">
                                            <input
                                              type="checkbox"
                                              checked={selectedValues.includes(normalized.value)}
                                              onInput={(event) => {
                                                const checked = event.currentTarget.checked;
                                                setDrafts((current) => {
                                                  const existing = Array.isArray(current[requestId]) ? current[requestId] : [];
                                                  const next = checked
                                                    ? [...existing, normalized.value]
                                                    : existing.filter((value) => value !== normalized.value);
                                                  return { ...current, [requestId]: next };
                                                });
                                              }}
                                            />
                                            <span className="space-y-1">
                                              <span className="block font-medium text-foreground">{normalized.label}</span>
                                              {normalized.description ? (
                                                <span className="block text-muted-foreground">{normalized.description}</span>
                                              ) : null}
                                            </span>
                                          </label>
                                        );
                                      })}
                                    </div>
                                  ) : (
                                    <SelectField
                                      value={Array.isArray(draftValue) ? draftValue[0] ?? "" : draftValue}
                                      onInput={(event) => setDrafts((current) => ({ ...current, [requestId]: event.currentTarget.value }))}
                                    >
                                      {options.map((option, optionIndex) => {
                                        const normalized = normalizeOption(option, optionIndex);
                                        return (
                                          <option key={normalized.key} value={normalized.value}>
                                            {normalized.label}
                                          </option>
                                        );
                                      })}
                                    </SelectField>
                                  )
                                ) : (
                                  <Textarea
                                    value={Array.isArray(draftValue) ? draftValue.join("\n") : draftValue}
                                    className="min-h-[8rem] rounded-2xl border-border/70 bg-background/80"
                                    onInput={(event) => setDrafts((current) => ({ ...current, [requestId]: event.currentTarget.value }))}
                                  />
                                ))}

                                {!askUserBridge && request.allow_freeform ? (
                                  <div className="space-y-2">
                                    <label className="text-sm font-medium text-foreground">Other response</label>
                                    <Textarea
                                      value={freeformValue}
                                      placeholder="Other response"
                                      className="min-h-[6rem] rounded-2xl border-border/70 bg-background/80"
                                      onInput={(event) =>
                                        setFreeformDrafts((current) => ({ ...current, [requestId]: event.currentTarget.value }))
                                      }
                                    />
                                  </div>
                                ) : null}

                                {sessionId ? (
                                  <div className="space-y-2">
                                    <div className="formActions gap-2">
                                      <Button
                                        type="button"
                                        disabled={Boolean(requestSubmittingById[requestId]) || Boolean(askUserBridge && !askUserBridgeReady)}
                                        onClick={() => {
                                          const payload =
                                            askUserBridge
                                              ? {
                                                  id: request.id,
                                                  value: encodeAskUserBridgeResponse(askUserBridgeAnswers),
                                                }
                                              : request.method === "confirm"
                                              ? { id: request.id, confirmed: true }
                                              : {
                                                  id: request.id,
                                                  value: mergeFreeformValue(request, normalizeRequestValue(request, draftValue), freeformValue),
                                                };
                                          void submitRequestResponse(requestId, payload);
                                        }}
                                      >
                                        {requestSubmittingById[requestId] ? "Submitting..." : "Confirm"}
                                      </Button>
                                      <Button
                                        type="button"
                                        variant="outline"
                                        disabled={Boolean(requestSubmittingById[requestId])}
                                        onClick={() => {
                                          void submitRequestResponse(requestId, {
                                            id: request.id,
                                            cancelled: true,
                                          });
                                        }}
                                      >
                                        Cancel
                                      </Button>
                                    </div>
                                    {requestErrorById[requestId] ? (
                                      <p className="text-sm font-medium text-destructive">{requestErrorById[requestId]}</p>
                                    ) : null}
                                  </div>
                                ) : null}
                              </CardContent>
                            </Card>
                          );
                        })
                      ) : (
                        <WorkspaceSection title="UI Requests">
                          <p className="text-sm text-muted-foreground">No pending requests.</p>
                        </WorkspaceSection>
                      )}
                    </div>
                  </ScrollArea>
                </TabsContent>
                <TabsContent value="supervisor" className="min-h-0">
                  <ScrollArea className="workspaceScroll h-full pr-1">
                    <p className="text-sm text-muted-foreground">Supervisor settings open in a dedicated dialog from the toolbar.</p>
                  </ScrollArea>
                </TabsContent>

                <TabsContent value="metadata" className="min-h-0">
                  <ScrollArea className="workspaceScroll h-full pr-1">
                    <WorkspaceSection title="Metadata" badge={metadataEntries.length ? `${metadataEntries.length}` : undefined}>
                      {metadataEntries.length ? renderKeyValueList(metadataEntries) : <p className="text-sm text-muted-foreground">No metadata available.</p>}
                    </WorkspaceSection>
                  </ScrollArea>
                </TabsContent>
                <TabsContent value="diagnostics" className="min-h-0">
                  <ScrollArea className="workspaceScroll h-full pr-1">
                    <WorkspaceSection title="Diagnostics" badge={diagnosticsEntries.length ? `${diagnosticsEntries.length}` : undefined}>
                      {detailEntries.length ? (
                        <div className="space-y-4">
                          {priorityDetailEntries.length ? renderKeyValueList(priorityDetailEntries) : null}
                          {genericDetailEntries.length ? renderKeyValueList(genericDetailEntries) : null}
                        </div>
                      ) : (
                        <p className="text-sm text-muted-foreground">No diagnostics available.</p>
                      )}
                    </WorkspaceSection>
                  </ScrollArea>
                </TabsContent>
                <TabsContent value="queue" className="min-h-0">
                  <ScrollArea className="workspaceScroll h-full pr-1">
                    <WorkspaceSection title="Queue" badge={queueItems.length ? `${queueItems.length}` : undefined}>
                      {queueItems.length ? (
                        <div className="space-y-3">
                          <ul className="workspaceCollection space-y-2 text-sm text-foreground">
                            {queueItems.map((item) => (
                              <li key={item.id} className="rounded-xl border border-border/60 bg-card/60 px-3 py-2">
                                {item.text}
                              </li>
                            ))}
                          </ul>
                          <Button type="button" variant="outline" size="sm" disabled={queueCancelling} onClick={() => { void cancelQueue(); }}>
                            {queueCancelling ? "Cancelling..." : "Cancel queued send"}
                          </Button>
                        </div>
                      ) : (
                        <p className="text-sm text-muted-foreground">No queued items.</p>
                      )}
                    </WorkspaceSection>
                  </ScrollArea>
                </TabsContent>
                {files.length ? (
                  <TabsContent value="files" className="min-h-0">
                    <ScrollArea className="workspaceScroll h-full pr-1">
                      <WorkspaceSection title="Files" badge={`${files.length}`}>
                        <ul className="workspaceCollection space-y-2 text-sm text-foreground">
                          {files.map((file) => (
                            <li key={file} className="rounded-xl border border-border/60 bg-card/60 px-3 py-2 font-mono text-xs sm:text-sm">
                              {file}
                            </li>
                          ))}
                        </ul>
                      </WorkspaceSection>
                    </ScrollArea>
                  </TabsContent>
                ) : null}
              </CardContent>
            </Tabs>
          ) : (
            <>
              <Separator className="bg-border/70" />
              <CardContent className="pt-4">
                <WorkspaceSection title="UI Requests">
                  <p className="text-sm text-muted-foreground">No pending requests.</p>
                </WorkspaceSection>
              </CardContent>
            </>
          )}
        </CardHeader>
      </Card>
    </aside>
  );
}
