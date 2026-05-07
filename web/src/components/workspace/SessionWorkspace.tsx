import type { ComponentChildren, JSX } from "preact";
import { useState } from "preact/hooks";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";

import { useSessionUiStore, useSessionsStore, useWaitsStore } from "../../app/providers";
import { api } from "../../lib/api";
import { calculateContextTokenUsage, type ContextTokenUsageResult } from "../../lib/context-token-usage";
import { backendCapability, normalizeLaunchBackend } from "../../lib/launch";
import { getSessionDisplayName } from "../../lib/session-display";
import type { BackendCapabilitySnapshot, MessageEvent, SessionSummary, SessionUiRequest } from "../../lib/types";

type MetadataEntry = [string, unknown];

interface SessionWorkspaceProps {
  mode?: "default" | "details";
  initialTab?: "metadata";
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

function pushEntry(entries: MetadataEntry[], key: string, value: unknown) {
  if (value == null) {
    return;
  }
  if (typeof value === "string" && value.trim().length === 0) {
    return;
  }
  entries.push([key, value]);
}

function sessionEntries(session: SessionSummary | null, sessionId: string | null): MetadataEntry[] {
  const entries: MetadataEntry[] = [];
  if (session) {
    pushEntry(entries, "display_name", getSessionDisplayName(session));
  }
  pushEntry(entries, "session_id", session?.session_id ?? sessionId);
  pushEntry(entries, "cwd", session?.cwd);
  pushEntry(entries, "git_branch", session?.git_branch);
  pushEntry(entries, "session_file_path", session?.session_file_path);
  pushEntry(entries, "backend_session_id", session?.backend_session_id);
  pushEntry(entries, "historical", session?.historical);
  return entries;
}

function runtimeEntries(session: SessionSummary | null, runtimeId: string | null, diagnostics: Record<string, unknown> | null): MetadataEntry[] {
  const entries: MetadataEntry[] = [];
  pushEntry(entries, "agent_backend", session?.agent_backend);
  pushEntry(entries, "runtime_id", session?.runtime_id ?? runtimeId);
  pushEntry(entries, "generation_id", session?.generation_id);
  pushEntry(entries, "transport_state", session?.transport_state);
  pushEntry(entries, "reset_required", session?.reset_required);
  pushEntry(entries, "transport_reason", session?.transport_reason);
  pushEntry(entries, "model", session?.model ?? diagnostics?.model);
  pushEntry(entries, "provider", session?.provider_choice ?? diagnostics?.provider);
  pushEntry(entries, "reasoning_effort", session?.reasoning_effort ?? diagnostics?.reasoning_effort);
  pushEntry(entries, "service_tier", session?.service_tier);
  pushEntry(entries, "busy", session?.busy);
  pushEntry(entries, "queue_len", session?.queue_len);
  pushEntry(entries, "focused", session?.focused);
  if (session?.iod) {
    pushEntry(entries, "iod_mode", session.iod.mode);
    pushEntry(entries, "iod_git_sha", session.iod.git_sha);
    pushEntry(entries, "iod_build_date", session.iod.build_date);
  }
  return entries;
}

const representedDiagnosticKeys = new Set([
  "agent_backend",
  "backend_session_id",
  "busy",
  "cwd",
  "display_name",
  "focused",
  "generation_id",
  "git_branch",
  "model",
  "provider",
  "provider_choice",
  "queue_len",
  "reasoning_effort",
  "reset_required",
  "runtime_id",
  "service_tier",
  "session_file_path",
  "session_id",
  "transport_reason",
  "transport_state",
]);

function diagnosticEntries(diagnostics: Record<string, unknown> | null, sessionHasFilePath: boolean): MetadataEntry[] {
  return entriesFromRecord(diagnostics)
    .filter(([key, value]) => key !== "todo_snapshot" && value != null)
    .filter(([key]) => key === "session_file_path" || !representedDiagnosticKeys.has(key))
    .filter(([key]) => !(key === "session_file_path" && sessionHasFilePath));
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
    case "display_name":
      return "Display name";
    case "agent_backend":
      return "Backend";
    case "transport_state":
      return "Transport";
    case "reset_required":
      return "Reset required";
    default:
      return key.replace(/_/g, " ").replace(/\b\w/g, (char) => char.toUpperCase());
  }
}

function formatDiagnosticValue(key: string, value: unknown): string {
  if ((key === "updated_ts" || key.endsWith("_ts")) && typeof value === "number" && Number.isFinite(value) && value > 1_000_000_000) {
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

function isMonospaceMetadataKey(key: string) {
  return key.endsWith("_id") || key.endsWith("_path") || key === "cwd" || key === "git_branch" || key === "log_path";
}

function renderKeyValueList(entries: MetadataEntry[]) {
  return (
    <dl className="workspaceMetadataList">
      {entries.map(([key, value]) => (
        <div key={key} className="workspaceMetadataRow">
          <dt>{formatDiagnosticLabel(key)}</dt>
          <dd className={isMonospaceMetadataKey(key) ? "break-all font-mono" : undefined}>{formatDiagnosticValue(key, value)}</dd>
        </div>
      ))}
    </dl>
  );
}

const capabilityEntries: Array<[keyof BackendCapabilitySnapshot, string]> = [
  ["launch_provider", "launch provider"],
  ["launch_model", "launch model"],
  ["launch_reasoning_effort", "launch reasoning"],
  ["runtime_streaming", "streaming"],
  ["runtime_tool_trace", "tool trace"],
  ["runtime_reasoning_trace", "reasoning trace"],
  ["runtime_context_usage", "context usage"],
  ["runtime_ui_requests", "UI requests"],
  ["runtime_interrupt", "interrupt"],
  ["runtime_probe", "state probe"],
  ["iod_stdio", "IOD stdio"],
  ["iod_unix", "IOD unix"],
  ["grpc", "gRPC"],
  ["supervisor", "supervisor"],
  ["resume_history", "resume history"],
  ["worktree", "worktree"],
];

function BackendCapabilitiesPanel({ backend, capabilities }: { backend: string; capabilities: BackendCapabilitySnapshot | null }) {
  return (
    <WorkspaceSection title="Backend Capabilities" badge={backend}>
      {capabilities ? (
        <div className="workspaceCapabilityGrid">
          {capabilityEntries.map(([key, label]) => {
            const enabled = capabilities[key] === true;
            return (
              <div key={key} className="workspaceCapabilityItem" data-enabled={enabled ? "true" : "false"}>
                <span>{label}</span>
                <Badge variant={enabled ? "secondary" : "outline"}>{enabled ? "yes" : "no"}</Badge>
              </div>
            );
          })}
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">No backend capability matrix available.</p>
      )}
    </WorkspaceSection>
  );
}


function WorkspaceSection({ title, badge, children }: { title: string; badge?: string; children: ComponentChildren }) {
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

function ContextUsagePanel({ calculating, error, usage, onCalculate }: { calculating: boolean; error: string; usage: ContextTokenUsageResult | null; onCalculate(): void }) {
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
            {calculating ? "Calculating" : "Calculate"}
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
              {usage.fallback ? usage.fallbackReason : "tokenizer: o200k_base"}
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

function requestLabel(request: SessionUiRequest, index: number) {
  return [request.title, request.label, request.question, request.message, request.method || `Request ${index + 1}`].filter((value): value is string => typeof value === "string" && value.trim().length > 0).join(" · ");
}

export function SessionWorkspace({ mode = "details", initialTab = "metadata" }: SessionWorkspaceProps) {
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
  const { items, activeSessionId, newSessionDefaults } = useSessionsStore();
  const waitsState = useWaitsStore();
  const workspaceSessionId = sessionId ?? activeSessionId;
  const activeSession = workspaceSessionId ? items.find((item) => item.session_id === workspaceSessionId) ?? null : null;
  const activeWait = workspaceSessionId ? waitsState.activeBySessionId[workspaceSessionId] ?? activeSession?.active_wait ?? null : null;
  const requests = Array.isArray(sessionUiState.requests) ? sessionUiState.requests : [];
  const files = Array.isArray(sessionUiState.files) ? sessionUiState.files : [];
  const queueItems = queueItemsFromValue(queue);
  const sessionMeta = sessionEntries(activeSession, workspaceSessionId);
  const runtimeMeta = runtimeEntries(activeSession, runtimeId, diagnostics);
  const diagnosticMeta = diagnosticEntries(diagnostics, typeof activeSession?.session_file_path === "string" && activeSession.session_file_path.trim().length > 0);
  const activeBackend = normalizeLaunchBackend(activeSession?.agent_backend ?? (typeof diagnostics?.agent_backend === "string" ? diagnostics.agent_backend : undefined));
  const activeBackendCapabilities = backendCapability(newSessionDefaults, activeBackend);
  const [contextUsageResult, setContextUsageResult] = useState<ContextTokenUsageResult | null>(null);
  const [contextUsageCalculating, setContextUsageCalculating] = useState(false);
  const [contextUsageError, setContextUsageError] = useState("");
  const compact = mode === "default";
  const panelLabel = initialTab === "metadata" ? "Metadata" : "Metadata";
  const hasWorkspaceData = sessionMeta.length > 0 || runtimeMeta.length > 0 || diagnosticMeta.length > 0 || queueItems.length > 0 || requests.length > 0 || files.length > 0 || Boolean(activeWait);

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
      const diagnosticsModel = diagnostics && typeof diagnostics.model === "string" ? diagnostics.model : "";
      setContextUsageResult(await calculateContextTokenUsage(events as MessageEvent[], activeSession?.model || diagnosticsModel));
    } catch (error) {
      setContextUsageError(error instanceof Error && error.message.trim() ? error.message : "Context usage calculation failed");
    } finally {
      setContextUsageCalculating(false);
    }
  };

  return (
    <aside className="workspacePane">
      <Card
        data-testid="workspace-card"
        data-mode={compact ? "default" : "details"}
        className="workspaceCard flex h-full min-h-0 flex-col rounded-[1.5rem] border-border/70 bg-card/95 shadow-lg shadow-primary/5"
      >
        <CardHeader className="space-y-4 pb-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="space-y-1">
              <CardTitle className="text-base">{panelLabel}</CardTitle>
              <p className="text-sm text-muted-foreground">Session, runtime, queue, request, and diagnostic metadata.</p>
            </div>
            <Badge variant={loading ? "default" : hasWorkspaceData ? "secondary" : "outline"}>
              {loading ? "Refreshing" : hasWorkspaceData ? "Live context" : "Quiet"}
            </Badge>
          </div>
        </CardHeader>
        <CardContent className="min-h-0 flex-1 p-0 px-6 pb-6">
          <ScrollArea className="workspaceScroll h-full pr-1">
            <div className="workspacePanelGrid grid gap-4 lg:grid-cols-2">
              <WorkspaceSection title="Session" badge={sessionMeta.length ? `${sessionMeta.length}` : undefined}>
                {sessionMeta.length ? renderKeyValueList(sessionMeta) : <p className="text-sm text-muted-foreground">No session metadata available.</p>}
              </WorkspaceSection>

              <WorkspaceSection title="Runtime" badge={runtimeMeta.length ? `${runtimeMeta.length}` : undefined}>
                {runtimeMeta.length ? renderKeyValueList(runtimeMeta) : <p className="text-sm text-muted-foreground">No runtime metadata available.</p>}
              </WorkspaceSection>

              <BackendCapabilitiesPanel backend={activeBackend} capabilities={activeBackendCapabilities} />

              <WorkspaceSection title="Queue" badge={queueItems.length ? `${queueItems.length}` : undefined}>
                {queueItems.length ? (
                  <ul className="workspaceCollection space-y-2 text-sm text-foreground">
                    {queueItems.map((item) => <li key={item.id} className="rounded-xl border border-border/60 bg-card/60 px-3 py-2">{item.text}</li>)}
                  </ul>
                ) : <p className="text-sm text-muted-foreground">No queued prompts.</p>}
              </WorkspaceSection>

              <WorkspaceSection title="UI Requests" badge={requests.length ? `${requests.length}` : undefined}>
                {requests.length ? (
                  <ul className="workspaceCollection space-y-2 text-sm text-foreground">
                    {requests.map((request, index) => <li key={request.id || request.request_id || index} className="rounded-xl border border-border/60 bg-card/60 px-3 py-2">{requestLabel(request, index)}</li>)}
                  </ul>
                ) : <p className="text-sm text-muted-foreground">No pending requests.</p>}
              </WorkspaceSection>

              <WorkspaceSection title="Files" badge={files.length ? `${files.length}` : undefined}>
                {files.length ? (
                  <ul className="workspaceCollection space-y-2 text-sm text-foreground">
                    {files.map((file) => <li key={file} className="break-all rounded-xl border border-border/60 bg-card/60 px-3 py-2 font-mono">{file}</li>)}
                  </ul>
                ) : <p className="text-sm text-muted-foreground">No tracked files.</p>}
              </WorkspaceSection>

              <WorkspaceSection title="Active Wait" badge={activeWait ? "1" : undefined}>
                {activeWait ? renderKeyValueList([
                  ["wait_id", activeWait.wait_id],
                  ["thread_id", activeWait.thread_id],
                  ["state", activeWait.state],
                  ["question", activeWait.question],
                ]) : <p className="text-sm text-muted-foreground">No active wait.</p>}
              </WorkspaceSection>

              <WorkspaceSection title="Diagnostics" badge={diagnosticMeta.length ? `${diagnosticMeta.length}` : undefined}>
                {diagnosticMeta.length ? renderKeyValueList(diagnosticMeta) : <p className="text-sm text-muted-foreground">No diagnostics available.</p>}
              </WorkspaceSection>

              <ContextUsagePanel
                calculating={contextUsageCalculating}
                error={contextUsageError}
                usage={contextUsageResult}
                onCalculate={() => { void calculateContextUsage(); }}
              />
            </div>
          </ScrollArea>
        </CardContent>
      </Card>
    </aside>
  );
}
