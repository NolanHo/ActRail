import { Fragment, h, type ComponentChildren } from "preact";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import { unified } from "unified";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

import { AskUserCard, askUserHistorySignature, isUnresolvedAskUserEvent } from "./AskUserCard";
import { TraceView } from "./TraceView";
import { buildToolActivitySummary, type MachineTraceKind, type ToolActivityEvent } from "./toolActivity";
import { WaitCard } from "../waits/WaitCard";
import {
  shallowEqual,
  useComposerStoreApi,
  useComposerStoreSelector,
  useLiveSessionStoreApi,
  useLiveSessionStoreSelector,
  useMessagesStoreApi,
  useMessagesStoreSelector,
  useSessionsStoreSelector,
} from "../../app/providers";
import { api } from "../../lib/api";
import { getSessionRuntimeId } from "../../lib/session-identity";
import type { MessageEvent, SessionSummary, TodoSnapshotItem } from "../../lib/types";

const EMPTY_PENDING_MESSAGES: never[] = [];
const EMPTY_MESSAGES: MessageEvent[] = [];

type ConversationActiveSession = Pick<SessionSummary, "session_id" | "runtime_id" | "agent_backend" | "historical" | "transport" | "busy" | "cwd">;

function selectConversationActiveSession(state: { activeSessionId: string | null; items: SessionSummary[] }): ConversationActiveSession | null {
  const session = state.items.find((item) => item.session_id === state.activeSessionId) ?? null;
  if (!session) {
    return null;
  }
  return {
    session_id: session.session_id,
    runtime_id: session.runtime_id,
    agent_backend: session.agent_backend,
    historical: session.historical,
    transport: session.transport,
    busy: session.busy,
    cwd: session.cwd,
  };
}

function conversationActiveSessionEqual(left: ConversationActiveSession | null, right: ConversationActiveSession | null) {
  if (left === right) {
    return true;
  }
  if (!left || !right) {
    return false;
  }
  return left.session_id === right.session_id
    && left.runtime_id === right.runtime_id
    && left.agent_backend === right.agent_backend
    && left.historical === right.historical
    && left.transport === right.transport
    && left.busy === right.busy
    && left.cwd === right.cwd;
}

const MAIN_TIMELINE_KINDS = new Set([
  "system",
  "user",
  "assistant",
  "ask_user",
  "wait",
  "reasoning",
  "tool",
  "tool_result",
  "team",
  "todo_snapshot",
  "custom_message",
  "pi_session",
  "pi_model_change",
  "pi_thinking_level_change",
  "pi_event",
  "event",
  "error",
]);

const MACHINE_TRACE_KINDS = new Set(["reasoning", "tool", "tool_result", "todo_snapshot"]);
const COMPACT_EVENT_KINDS = new Set(["event", "pi_event"]);
const PI_EVENT_COMPACT_VARIANTS = {
  turn_terminal: "turn_terminal",
  empty_output: "empty_output",
  retry_error: "retry_error",
  compaction: "compaction",
  extension_ui: "extension_ui",
} as const;
const CHAT_GROUPABLE_KINDS = new Set(["user", "assistant", "ask_user"]);
type CompactTraceKind = "reasoning" | "tool" | "tool_result" | "todo_snapshot" | "custom_message" | "pi_event";
interface AssistantTurnMeta {
  tools: number;
  ok: number;
  errors: number;
  maxToolCallSeconds: number | null;
  turnSeconds: number | null;
  finishedTs: number | null;
}

type ToolPairState = "call" | "result" | "orphan";

interface ToolTracePairInfo {
  id: string;
  state: ToolPairState;
}

interface BackendToolActivitySummary {
  operations: number;
  total_tools: number;
  tool_calls: number;
  tool_results: number;
  running: number;
  ok: number;
  failed: number;
  reasoning: number;
  todo_snapshots: number;
  process_updates: number;
  system_events: number;
  started_at?: number;
  last_activity_at?: number;
  elapsed_seconds?: number;
  max_tool_call_seconds?: number;
  running_tool_names?: string[];
  summary_text?: string;
  status_text?: string;
}
const COLLAPSIBLE_LINE_THRESHOLD = 8;
const COLLAPSIBLE_CHAR_THRESHOLD = 420;
const MACHINE_TRACE_VISIBLE_LIMIT = 12;

const EVENT_LABELS: Record<string, string> = {
  system: "System",
  ask_user: "Question",
  wait: "Wait",
  reasoning: "Reasoning",
  tool: "Tool",
  tool_result: "Tool Result",
  team: "Team",
  todo_snapshot: "Todo Progress",
  custom_message: "Custom Message",
  pi_session: "Session",
  pi_model_change: "Model Change",
  pi_thinking_level_change: "Thinking Level",
  pi_event: "System Event",
  event: "Event",
  error: "Error",
};

interface MarkdownRenderOptions {
  sessionId?: string;
  cwd?: string;
  onOpenLocalFile?: (path: string, line?: number | null) => void;
}

function baseName(value: string): string {
  const normalized = value.replace(/[\\/]+$/, "");
  const parts = normalized.split(/[\\/]+/);
  return parts[parts.length - 1] || normalized;
}

function normalizePathSeparators(value: string): string {
  return value.replace(/\\/g, "/");
}

function isProbablyUrl(value: string): boolean {
  return /^[a-z][a-z0-9+.-]*:/i.test(value);
}

function isAbsolutePath(value: string): boolean {
  return value.startsWith("/") || /^[A-Za-z]:[\\/]/.test(value) || value.startsWith("~/");
}

function joinPaths(baseDir: string, target: string): string {
  const baseParts = normalizePathSeparators(baseDir).split("/").filter(Boolean);
  const targetParts = normalizePathSeparators(target).split("/");
  for (const part of targetParts) {
    if (!part || part === ".") continue;
    if (part === "..") {
      baseParts.pop();
      continue;
    }
    baseParts.push(part);
  }
  return `${baseDir.startsWith("/") ? "/" : ""}${baseParts.join("/")}` || "/";
}

function resolvePathTarget(rawTarget: string, cwd?: string): string {
  const target = rawTarget.trim();
  if (!target) return "";
  if (isProbablyUrl(target)) return target;
  if (isAbsolutePath(target)) return normalizePathSeparators(target);
  if (!cwd) return normalizePathSeparators(target);
  return joinPaths(cwd, target);
}

function parseLocalFileRef(rawValue: string, cwd?: string): { path: string; line?: number } | null {
  const trimmed = rawValue.trim();
  if (!trimmed || isProbablyUrl(trimmed) || trimmed.endsWith(":")) {
    return null;
  }

  let pathPart = trimmed;
  let line: number | undefined;

  const hashMatch = pathPart.match(/^(.*)#L(\d+)(?:-\d+)?$/i);
  if (hashMatch) {
    pathPart = hashMatch[1] || "";
    line = Number.parseInt(hashMatch[2] || "", 10);
  } else {
    const lineMatch = pathPart.match(/^(.*):(\d+)$/);
    if (lineMatch) {
      pathPart = lineMatch[1] || "";
      line = Number.parseInt(lineMatch[2] || "", 10);
    }
  }

  const resolvedPath = resolvePathTarget(pathPart, cwd);
  if (!resolvedPath || isProbablyUrl(resolvedPath)) {
    return null;
  }

  return Number.isFinite(line) ? { path: resolvedPath, line } : { path: resolvedPath };
}

function fileBlobHref(sessionId: string, path: string): string {
  return `api/sessions/${encodeURIComponent(sessionId)}/file/blob?path=${encodeURIComponent(path)}`;
}

function normalizeLineNumber(value: string | null): number | undefined {
  const line = Number.parseInt(String(value || "").trim(), 10);
  return Number.isFinite(line) && line > 0 ? line : undefined;
}

function rewriteOaiMemCitations(rawText: string): string {
  const raw = String(rawText ?? "");
  if (!raw.includes("<oai-mem-citation>")) {
    return raw;
  }

  const blockRegex = /<oai-mem-citation>\s*<citation_entries>\s*([\s\S]*?)\s*<\/citation_entries>\s*<rollout_ids>[\s\S]*?<\/rollout_ids>\s*<\/oai-mem-citation>/g;
  return raw.replace(blockRegex, (_whole, body) => {
    const lines = String(body || "")
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean);
    if (!lines.length) {
      return _whole;
    }

    const items = lines.map((line) => {
      const match = line.match(/^(.*?):(\d+)(?:-(\d+))?\|note=\[(.*)\]$/);
      if (!match) {
        return null;
      }
      const relPath = String(match[1] || "").trim().replace(/^\.?\//, "");
      const startLine = normalizeLineNumber(match[2]);
      const endLine = normalizeLineNumber(match[3]);
      const note = String(match[4] || "").trim();
      if (!relPath || !startLine || !note) {
        return null;
      }
      const lineSuffix = endLine && endLine >= startLine ? `#L${startLine}-${endLine}` : `#L${startLine}`;
      return `${note}|~/.codex/memories/${relPath}${lineSuffix}`;
    });

    if (items.some((item) => !item)) {
      return _whole;
    }

    const list = items
      .map((item, index) => {
        const [note, target] = String(item).split("|");
        return `${index + 1}. [${note}](${target})`;
      })
      .join("\n");

    return `\n---\n\nMemory citations:\n${list}`;
  });
}

type MarkdownNode = {
  type: string;
  alt?: string | null;
  checked?: boolean | null;
  children?: MarkdownNode[];
  depth?: number;
  identifier?: string;
  lang?: string | null;
  ordered?: boolean;
  start?: number | null;
  title?: string | null;
  url?: string;
  value?: string;
  align?: Array<"left" | "right" | "center" | null>;
};

type MarkdownDefinition = {
  title?: string | null;
  url: string;
};

const markdownProcessor = unified().use(remarkParse).use(remarkGfm).use(remarkBreaks);

function textFromChildren(children: ComponentChildren): string {
  if (children == null || typeof children === "boolean") {
    return "";
  }
  if (typeof children === "string" || typeof children === "number") {
    return String(children);
  }
  if (Array.isArray(children)) {
    return children.map((child) => textFromChildren(child)).join("");
  }
  if (typeof children === "object" && "props" in children) {
    return textFromChildren((children as { props?: { children?: ComponentChildren } }).props?.children ?? null);
  }
  return "";
}

function definitionId(value: string | undefined): string {
  return String(value || "").trim().toLowerCase();
}

function collectMarkdownDefinitions(root: MarkdownNode): Map<string, MarkdownDefinition> {
  const definitions = new Map<string, MarkdownDefinition>();
  for (const child of root.children || []) {
    if (child.type !== "definition") {
      continue;
    }
    const key = definitionId(child.identifier);
    if (!key || !child.url) {
      continue;
    }
    definitions.set(key, { url: child.url, title: child.title });
  }
  return definitions;
}

function renderMarkdownLink(target: string, children: ComponentChildren, options: MarkdownRenderOptions, title?: string | null) {
  const fileRef = parseLocalFileRef(target, options.cwd);
  if (fileRef && options.sessionId) {
    const displayLabel = textFromChildren(children).trim() || baseName(fileRef.path);
    const text = fileRef.line && displayLabel === baseName(fileRef.path) ? `${displayLabel}#L${fileRef.line}` : displayLabel;
    return (
      <a
        className="messageFileLink underline decoration-dotted underline-offset-4"
        data-file-path={fileRef.path}
        data-file-line={fileRef.line ? String(fileRef.line) : undefined}
        href={fileBlobHref(options.sessionId, fileRef.path)}
        rel="noreferrer"
        target="_blank"
        title={title || undefined}
      >
        {text}
      </a>
    );
  }

  const resolvedHref = resolvePathTarget(target, options.cwd);
  return (
    <a
      className="messageInlineLink underline decoration-dotted underline-offset-4"
      href={resolvedHref}
      rel="noreferrer"
      target="_blank"
      title={title || undefined}
    >
      {children}
    </a>
  );
}

function renderMarkdownImage(target: string, altText: string, options: MarkdownRenderOptions, title?: string | null) {
  const resolvedPath = resolvePathTarget(target, options.cwd);
  const src = options.sessionId && !isProbablyUrl(resolvedPath) ? fileBlobHref(options.sessionId, resolvedPath) : resolvedPath;
  return (
    <img
      alt={altText}
      className="messageImage max-h-80 rounded-2xl border border-border/60 bg-background/70 object-contain"
      loading="lazy"
      src={src}
      title={title || undefined}
    />
  );
}

function renderMarkdownChildren(children: MarkdownNode[] | undefined, options: MarkdownRenderOptions, definitions: Map<string, MarkdownDefinition>, keyPrefix: string): ComponentChildren {
  return (children || []).map((child, index) => (
    <Fragment key={`${keyPrefix}-${index}`}>{renderMarkdownNode(child, options, definitions, `${keyPrefix}-${index}`)}</Fragment>
  ));
}

function renderMarkdownTable(node: MarkdownNode, options: MarkdownRenderOptions, definitions: Map<string, MarkdownDefinition>, keyPrefix: string) {
  const rows = node.children || [];
  const headerRow = rows[0];
  const bodyRows = rows.slice(1);
  const alignments = Array.isArray(node.align) ? node.align : [];

  return (
    <div className="mdTableWrap overflow-x-auto rounded-2xl border border-border/60 bg-background/70">
      <table>
        {headerRow ? (
          <thead>
            <tr>
              {(headerRow.children || []).map((cell, index) => (
                <th key={`${keyPrefix}-head-${index}`} style={alignments[index] ? { textAlign: alignments[index] } : undefined}>
                  {renderMarkdownChildren(cell.children, options, definitions, `${keyPrefix}-head-${index}`)}
                </th>
              ))}
            </tr>
          </thead>
        ) : null}
        {bodyRows.length ? (
          <tbody>
            {bodyRows.map((row, rowIndex) => (
              <tr key={`${keyPrefix}-row-${rowIndex}`}>
                {(row.children || []).map((cell, cellIndex) => (
                  <td key={`${keyPrefix}-row-${rowIndex}-cell-${cellIndex}`} style={alignments[cellIndex] ? { textAlign: alignments[cellIndex] } : undefined}>
                    {renderMarkdownChildren(cell.children, options, definitions, `${keyPrefix}-row-${rowIndex}-cell-${cellIndex}`)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        ) : null}
      </table>
    </div>
  );
}

function renderMarkdownNode(node: MarkdownNode, options: MarkdownRenderOptions, definitions: Map<string, MarkdownDefinition>, keyPrefix: string): ComponentChildren {
  switch (node.type) {
    case "root":
      return renderMarkdownChildren(node.children, options, definitions, keyPrefix);
    case "definition":
      return null;
    case "paragraph":
      return <p>{renderMarkdownChildren(node.children, options, definitions, keyPrefix)}</p>;
    case "text":
    case "html":
      return node.value || "";
    case "strong":
      return <strong>{renderMarkdownChildren(node.children, options, definitions, keyPrefix)}</strong>;
    case "emphasis":
      return <em>{renderMarkdownChildren(node.children, options, definitions, keyPrefix)}</em>;
    case "delete":
      return <del>{renderMarkdownChildren(node.children, options, definitions, keyPrefix)}</del>;
    case "break":
      return <br />;
    case "inlineCode":
      return <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.92em]">{(node.value || "").replace(/\n$/, "")}</code>;
    case "code": {
      const className = node.lang ? `language-${node.lang}` : undefined;
      return (
        <pre className="overflow-x-auto rounded-2xl border border-border/60 bg-background/70 p-4">
          <code className={cn("font-mono text-sm", className)}>{(node.value || "").replace(/\n$/, "")}</code>
        </pre>
      );
    }
    case "heading": {
      const depth = Math.min(Math.max(node.depth || 1, 1), 6);
      return h(`h${depth}`, null, renderMarkdownChildren(node.children, options, definitions, keyPrefix));
    }
    case "blockquote":
      return <blockquote className="border-l-2 border-border/70 pl-4 text-muted-foreground">{renderMarkdownChildren(node.children, options, definitions, keyPrefix)}</blockquote>;
    case "list": {
      const ListTag = node.ordered ? "ol" : "ul";
      const listClassName = node.ordered
        ? "my-4 list-decimal space-y-1 pl-6"
        : "my-4 list-disc space-y-1 pl-6";
      return (
        <ListTag
          className={listClassName}
          start={node.ordered && node.start && node.start !== 1 ? node.start : undefined}
        >
          {renderMarkdownChildren(node.children, options, definitions, keyPrefix)}
        </ListTag>
      );
    }
    case "listItem": {
      const checked = typeof node.checked === "boolean" ? node.checked : null;
      return (
        <li className={checked === null ? "pl-1" : "flex items-start gap-2 pl-1"}>
          {checked === null ? null : <input checked={checked} className="mt-1" disabled readOnly type="checkbox" />}
          {renderMarkdownChildren(node.children, options, definitions, `${keyPrefix}-item`)}
        </li>
      );
    }
    case "thematicBreak":
      return <hr />;
    case "link":
      return renderMarkdownLink(node.url || "", renderMarkdownChildren(node.children, options, definitions, keyPrefix), options, node.title);
    case "image":
      return renderMarkdownImage(node.url || "", node.alt || "", options, node.title);
    case "linkReference": {
      const definition = definitions.get(definitionId(node.identifier));
      if (!definition) {
        return renderMarkdownChildren(node.children, options, definitions, keyPrefix);
      }
      return renderMarkdownLink(definition.url, renderMarkdownChildren(node.children, options, definitions, keyPrefix), options, definition.title);
    }
    case "imageReference": {
      const definition = definitions.get(definitionId(node.identifier));
      if (!definition) {
        return node.alt || "";
      }
      return renderMarkdownImage(definition.url, node.alt || "", options, definition.title);
    }
    case "table":
      return renderMarkdownTable(node, options, definitions, keyPrefix);
    default:
      if (node.children?.length) {
        return renderMarkdownChildren(node.children, options, definitions, keyPrefix);
      }
      return node.value || "";
  }
}

function MarkdownContent({ value, options = {} }: { value: string; options?: MarkdownRenderOptions }) {
  const parsed = useMemo(() => {
    const normalized = rewriteOaiMemCitations(value).replace(/\r\n?/g, "\n");
    const root = markdownProcessor.runSync(markdownProcessor.parse(normalized)) as MarkdownNode;
    return { root, definitions: collectMarkdownDefinitions(root) };
  }, [value]);
  const rendered = useMemo(
    () => renderMarkdownNode(parsed.root, options, parsed.definitions, "md"),
    [parsed, options.sessionId, options.cwd, options.onOpenLocalFile],
  );
  return <>{rendered}</>;
}

function messageContentParts(event: MessageEvent): string[] {
  const message = event.message;
  if (!message || !Array.isArray(message.content)) {
    return [];
  }
  return message.content
    .map((item) => (typeof item?.text === "string" ? item.text.trim() : ""))
    .filter(Boolean);
}

function firstNonEmptyText(...values: Array<unknown>): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}


function detailsSummary(details: Record<string, unknown> | undefined): string {
  if (!details) {
    return "";
  }
  if (typeof details.summary === "string" && details.summary.trim()) {
    return details.summary.trim();
  }
  if (typeof details.error === "string" && details.error.trim()) {
    return details.error.trim();
  }
  if (typeof details.errorMessage === "string" && details.errorMessage.trim()) {
    return details.errorMessage.trim();
  }
  if (typeof details.message === "string" && details.message.trim()) {
    return details.message.trim();
  }
  if (Array.isArray(details.todos) && details.todos.length) {
    return `${details.todos.length} todo item${details.todos.length === 1 ? "" : "s"}`;
  }
  const keys = Object.keys(details);
  if (keys.length) {
    return `Details: ${keys.join(", ")}`;
  }
  return "";
}

function contentTextFromMessage(event: MessageEvent): string {
  const kind = eventKind(event);
  if (kind === "ask_user") {
    return firstNonEmptyText(event.question, event.text, "Prompt");
  }
  if (typeof event.text === "string" && event.text.trim()) {
    return event.text;
  }
  const contentParts = messageContentParts(event);
  if (contentParts.length) {
    return contentParts.join("\n");
  }
  if (typeof event.output === "string" && event.output.trim()) {
    return event.output;
  }
  if (typeof event.summary === "string" && event.summary.trim()) {
    return event.summary;
  }
  if (typeof event.question === "string" && event.question.trim()) {
    return event.question;
  }
  if (typeof event.context === "string" && event.context.trim()) {
    return event.context;
  }
  if (event.details) {
    return detailsSummary(event.details) || JSON.stringify(event.details, null, 2);
  }
  return JSON.stringify(event, null, 2);
}

function eventKind(event: MessageEvent): string {
  if (event.is_error === true && typeof event.type === "string" && event.type) {
    return event.type;
  }
  if (typeof event.role === "string" && event.role) {
    return event.role;
  }
  if (typeof event.message?.role === "string" && event.message.role) {
    return event.message.role;
  }
  if (event.toolName === "ask_user") {
    return "ask_user";
  }
  if (typeof event.type === "string" && event.type.startsWith("wait.")) {
    return "wait";
  }
  if (typeof event.wait_id === "string" && typeof event.thread_id === "string" && typeof event.question === "string") {
    return "wait";
  }
  return typeof event.type === "string" && event.type ? event.type : "event";
}

function isPendingUserEvent(event: MessageEvent): boolean {
  return event.role === "user" && (event.bridge_pseudo === true || event.pending === true);
}

function isPiConfirmedUserEvent(event: MessageEvent): boolean {
  return event.role === "user" && typeof event.event_id === "string" && event.event_id.startsWith("pi:");
}

function durableUserAcknowledgesPending(durable: MessageEvent, pending: MessageEvent, requirePiConfirmation = false): boolean {
  if (durable.role !== "user" || isPendingUserEvent(durable) || typeof durable.text !== "string" || durable.text !== pending.text) {
    return false;
  }
  if (requirePiConfirmation && !isPiConfirmedUserEvent(durable)) {
    return false;
  }
  const pendingTS = eventTimestampSeconds(pending);
  const durableTS = eventTimestampSeconds(durable);
  if (pendingTS !== null) {
    return durableTS !== null && durableTS >= pendingTS - 2;
  }
  return true;
}

function filterResolvedBridgePseudoEvents(events: MessageEvent[], requirePiConfirmation = false): MessageEvent[] {
  const pendingBridgeEvents = events.filter(isPendingUserEvent);
  if (!pendingBridgeEvents.length) {
    return events;
  }

  const durableUsers = events
    .filter((event) => event.role === "user" && !isPendingUserEvent(event) && typeof event.text === "string");
  const failedRequestIds = new Set(
    events
      .filter((event) => typeof event.request_id === "string" && event.request_state === "failed")
      .map((event) => String(event.request_id)),
  );
  const failedTexts = new Set(
    events
      .filter((event) => event.request_state === "failed" && typeof event.pending_text === "string")
      .map((event) => String(event.pending_text)),
  );

  const acknowledgedEventIds = new Set<string>();
  let durableIdx = durableUsers.length - 1;
  let pendingIdx = pendingBridgeEvents.length - 1;
  while (durableIdx >= 0 && pendingIdx >= 0) {
    const requirePi = requirePiConfirmation && pendingBridgeEvents[pendingIdx].bridge_pseudo !== true;
    if (durableUserAcknowledgesPending(durableUsers[durableIdx], pendingBridgeEvents[pendingIdx], requirePi)) {
      const eventId = String(pendingBridgeEvents[pendingIdx]?.event_id || "").trim();
      if (eventId) {
        acknowledgedEventIds.add(eventId);
      }
      pendingIdx -= 1;
    }
    durableIdx -= 1;
  }

  for (const event of pendingBridgeEvents) {
    const eventId = String(event.event_id || "").trim();
    if (!eventId) {
      continue;
    }
    if (event.request_state === "failed" || event.resolved === true) {
      acknowledgedEventIds.add(eventId);
      continue;
    }
    if ((typeof event.request_id === "string" && failedRequestIds.has(String(event.request_id))) || (typeof event.text === "string" && failedTexts.has(String(event.text)))) {
      acknowledgedEventIds.add(eventId);
    }
  }

  return events.filter((event) => {
    if (!isPendingUserEvent(event)) {
      return true;
    }
    const eventId = String(event.event_id || "").trim();
    if (!eventId) {
      const requirePi = requirePiConfirmation && event.bridge_pseudo !== true;
      return !durableUsers.some((durable) => durableUserAcknowledgesPending(durable, event, requirePi));
    }
    return !acknowledgedEventIds.has(eventId);
  });
}

function filterLocalUserEchoes(events: MessageEvent[]): MessageEvent[] {
  const pendingUsers = events.filter(isPendingUserEvent);
  const piUsers = events.filter(isPiConfirmedUserEvent);
  const durableUsers = events.filter((event) => event.role === "user" && !isPendingUserEvent(event));
  if (!pendingUsers.length && !piUsers.length && durableUsers.length < 2) {
    return events;
  }
  return events.filter((event) => {
    if (event.role !== "user" || isPendingUserEvent(event) || isPiConfirmedUserEvent(event)) {
      return true;
    }
    if (pendingUsers.some((pending) => typeof pending.text === "string" && durableUserAcknowledgesPending(event, pending))) {
      return false;
    }
    if (piUsers.some((piUser) => durableUserAcknowledgesPending(piUser, event))) {
      return false;
    }
    const eventTS = eventTimestampSeconds(event);
    const laterDuplicate = durableUsers.some((other) => other !== event
      && !isPiConfirmedUserEvent(other)
      && typeof other.text === "string"
      && typeof event.text === "string"
      && other.text === event.text
      && eventTimestampSeconds(other) !== null
      && eventTS !== null
      && (eventTimestampSeconds(other) ?? 0) > eventTS
      && (eventTimestampSeconds(other) ?? 0) - eventTS <= 2);
    return !laterDuplicate;
  });
}

function shouldRenderInMainConversation(event: MessageEvent): boolean {
  const kind = eventKind(event);
  if (MAIN_TIMELINE_KINDS.has(kind)) {
    return true;
  }
  return Boolean(firstNonEmptyText(event.text, event.summary, event.question, event.context));
}

function canGroupEvent(kind: string): boolean {
  return CHAT_GROUPABLE_KINDS.has(kind);
}

function isProcessUpdateCustomMessage(event: MessageEvent): boolean {
  return eventKind(event) === "custom_message"
    && typeof event.custom_type === "string"
    && event.custom_type.startsWith("ad-process:");
}

function isCodexSubagentMessage(event: MessageEvent): boolean {
  const details = asRecord(event.details);
  return eventKind(event) === "custom_message"
    && typeof details?.custom_type === "string"
    && details.custom_type === "codex-subagent-message";
}

function piEventCompactVariant(event: MessageEvent): (typeof PI_EVENT_COMPACT_VARIANTS)[keyof typeof PI_EVENT_COMPACT_VARIANTS] | null {
  if (eventKind(event) !== "pi_event") {
    return null;
  }
  const details = asRecord(event.details);
  if (details?.raw_type === "extension_ui_request") {
    return PI_EVENT_COMPACT_VARIANTS.extension_ui;
  }
  if (details?.raw_type === "compaction_start" || details?.raw_type === "compaction_end" || asRecord(details?.compaction)) {
    return PI_EVENT_COMPACT_VARIANTS.compaction;
  }
  const summary = firstNonEmptyText(event.summary, event.text).toLowerCase();
  if (!summary) {
    return null;
  }
  if (summary.includes("turn finished without assistant output")) {
    return PI_EVENT_COMPACT_VARIANTS.turn_terminal;
  }
  if (summary.includes("assistant returned empty message")) {
    return PI_EVENT_COMPACT_VARIANTS.empty_output;
  }
  if (summary.includes("retry") || summary.includes("rate limit") || summary.includes("429")) {
    return PI_EVENT_COMPACT_VARIANTS.retry_error;
  }
  if (summary.includes("compaction") || summary.includes("compacting")) {
    return PI_EVENT_COMPACT_VARIANTS.compaction;
  }
  return null;
}

function compactTraceKind(event: MessageEvent): CompactTraceKind | null {
  const kind = eventKind(event);
  if (MACHINE_TRACE_KINDS.has(kind)) {
    return kind as CompactTraceKind;
  }
  if (isProcessUpdateCustomMessage(event) && !isCodexSubagentMessage(event)) {
    return "custom_message";
  }
  if (piEventCompactVariant(event) || COMPACT_EVENT_KINDS.has(kind)) {
    return "pi_event";
  }
  return null;
}

function isMachineTraceKind(kind: string): kind is CompactTraceKind {
  return MACHINE_TRACE_KINDS.has(kind) || kind === "custom_message" || kind === "pi_event";
}

function eventLabel(kind: string): string {
  return EVENT_LABELS[kind] || kind.replace(/_/g, " ").replace(/\b\w/g, (char) => char.toUpperCase());
}

function surfaceBadgeVariant(kind: string): "default" | "secondary" | "outline" {
  switch (kind) {
    case "user":
      return "default";
    case "system":
    case "assistant":
    case "tool_result":
    case "todo_snapshot":
      return "secondary";
    default:
      return "outline";
  }
}

function messageSurfaceTone(kind: string, isError = false): string {
  if (isError) {
    return "messageToneError";
  }

  switch (kind) {
    case "user":
      return "messageToneUser text-foreground";
    case "system":
      return "messageToneSystem";
    case "assistant":
      return "messageToneAssistant";
    case "ask_user":
      return "messageToneAskUser";
    case "reasoning":
      return "messageToneReasoning";
    case "tool":
      return "messageToneTool";
    case "tool_result":
      return "messageToneToolResult";
    case "team":
      return "messageToneTeam";
    case "todo_snapshot":
      return "";
    default:
      return "messageToneDefault";
  }
}

function isDisplayableEpochTs(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 1_000_000_000;
}

function eventTimestampSeconds(event: MessageEvent): number | null {
  for (const key of ["ts", "timestamp", "created_at", "updated_at"] as const) {
    const value = event[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value > 1_000_000_000_000 ? value / 1000 : value;
    }
    if (typeof value === "string" && value.trim()) {
      const parsed = Date.parse(value);
      if (Number.isFinite(parsed)) {
        return parsed / 1000;
      }
    }
  }
  return null;
}

function sortEventsByTimestamp<T extends MessageEvent>(events: T[]): T[] {
  if (events.length <= 1) {
    return events;
  }
  let lastTs: number | null = null;
  const rows = events.map((event, index) => {
    const parsedTs = eventTimestampSeconds(event);
    const ts = parsedTs ?? (lastTs != null ? lastTs + 1e-6 : (index + 1) * 1e-6);
    if (lastTs == null || ts > lastTs) {
      lastTs = ts;
    }
    return { event, index, ts };
  });
  rows.sort((a, b) => {
    if (a.ts !== b.ts) {
      return a.ts - b.ts;
    }
    return a.index - b.index;
  });
  return rows.map((row) => row.event);
}

function sortMachineTraceEvents(events: MessageEvent[]): MessageEvent[] {
  return sortEventsByTimestamp(events);
}

function machineTraceKindForActivity(event: MessageEvent): MachineTraceKind | null {
  return compactTraceKind(event) as MachineTraceKind | null;
}

function formatMessageTimestamp(ts: number): string {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(ts * 1000));
}

function formatTurnMetaTimestamp(ts: number): string {
  return new Intl.DateTimeFormat("zh-CN", {
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(ts * 1000));
}

function formatDuration(seconds: number | null): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) {
    return "-";
  }
  const total = Math.max(0, Math.floor(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const parts: string[] = [];
  if (h) parts.push(`${h}h`);
  if (m || h) parts.push(`${m}m`);
  parts.push(`${s}s`);
  return parts.join("");
}

function messageDayKey(ts: number): string {
  const date = new Date(ts * 1000);
  return `${date.getFullYear()}-${date.getMonth() + 1}-${date.getDate()}`;
}

function formatDaySeparator(ts: number): string {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  }).format(new Date(ts * 1000));
}

function handleRichTextClick(event: MouseEvent, options: MarkdownRenderOptions) {
  if (!options.onOpenLocalFile) {
    return;
  }

  const target = event.target instanceof Element ? event.target : null;
  const link = target?.closest("a[data-file-path]") as HTMLAnchorElement | null;
  if (!link) {
    return;
  }

  const path = String(link.getAttribute("data-file-path") || "").trim();
  if (!path) {
    return;
  }

  event.preventDefault();
  options.onOpenLocalFile(path, normalizeLineNumber(link.getAttribute("data-file-line")));
}

function renderRichText(value: string, className = "messageBody", options: MarkdownRenderOptions = {}) {
  if (!value.trim()) {
    return null;
  }
  return (
    <div className={className} onClick={(event) => handleRichTextClick(event as MouseEvent, options)}>
      <MarkdownContent value={value} options={options} />
    </div>
  );
}

function shouldCollapseContent(value: string): boolean {
  const normalized = value.trim();
  if (!normalized) {
    return false;
  }
  const lineCount = normalized.split("\n").length;
  return lineCount > COLLAPSIBLE_LINE_THRESHOLD || normalized.length > COLLAPSIBLE_CHAR_THRESHOLD;
}

function compactSingleLine(value: string, maxLength = 140): string {
  const normalized = value.replace(/\s+/g, " ").trim();
  if (!normalized) {
    return "";
  }
  const chars = Array.from(normalized);
  if (chars.length <= maxLength) {
    return normalized;
  }
  return `${chars.slice(0, Math.max(0, maxLength - 1)).join("").trimEnd()}...`;
}

function ExpandableRichText({
  value,
  className = "messageBody",
  options = {},
  forceCollapsible = false,
}: {
  value: string;
  className?: string;
  options?: MarkdownRenderOptions;
  forceCollapsible?: boolean;
}) {
  const collapsible = forceCollapsible || shouldCollapseContent(value);
  const [expanded, setExpanded] = useState(false);
  const previousValueRef = useRef(value);
  const contentClassName = cn("messageExpandableContent", collapsible && !expanded && "isCollapsed");

  useEffect(() => {
    if (previousValueRef.current !== value) {
      previousValueRef.current = value;
      setExpanded(false);
    }
  }, [value]);

  return (
    <div className="messageExpandable space-y-3">
      <div className={contentClassName}>{renderRichText(value, className, options)}</div>
      {collapsible ? (
        <button
          type="button"
          className="messageExpandButton inline-flex items-center rounded-full border border-border/70 px-3 py-1 text-xs font-medium text-muted-foreground transition hover:bg-accent hover:text-accent-foreground"
          aria-expanded={expanded ? "true" : "false"}
          onClick={() => setExpanded((current) => !current)}
        >
          {expanded ? "Show less" : "Show more"}
        </button>
      ) : null}
    </div>
  );
}

function SystemPromptCard({ event, options }: { event: MessageEvent; options: MarkdownRenderOptions }) {
  const text = contentTextFromMessage(event);
  return (
    <MessageSurface kind="system">
      {renderCardHeader("system", undefined, undefined, event.ts)}
      <ExpandableRichText key={text} value={text} options={options} forceCollapsible />
    </MessageSurface>
  );
}

function renderCardHeader(kind: string, title?: string, summary?: string, ts?: number) {
  const showTimestamp = isDisplayableEpochTs(ts);
  return (
    <header className="messageCardHeader flex flex-col gap-2">
      <div className="messageCardHeaderRow flex flex-wrap items-center gap-2">
        <Badge variant={surfaceBadgeVariant(kind)}>{eventLabel(kind)}</Badge>
        {title ? <div className="messageCardTitle text-sm font-semibold text-foreground">{title}</div> : null}
        {showTimestamp ? (
          <time className="messageTimestamp ml-auto text-xs text-muted-foreground" dateTime={new Date(ts * 1000).toISOString()}>
            {formatMessageTimestamp(ts)}
          </time>
        ) : null}
      </div>
      {summary ? <div className="messageCardSummary text-sm text-muted-foreground">{summary}</div> : null}
    </header>
  );
}

async function writeClipboardText(text: string) {
  const value = text.trim();
  if (!value) {
    return false;
  }
  if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch (_error) {
      // Fall through to execCommand for browsers that expose clipboard but deny it outside secure contexts.
    }
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "true");
  textarea.style.position = "fixed";
  textarea.style.top = "-1000px";
  textarea.style.left = "-1000px";
  document.body.appendChild(textarea);
  textarea.select();
  textarea.setSelectionRange(0, value.length);
  let copied = false;
  try {
    copied = typeof document.execCommand === "function" && document.execCommand("copy") === true;
  } finally {
    textarea.remove();
  }
  return copied;
}

function CopyMessageIcon() {
  return (
    <svg className="messageCopyIcon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="9" y="9" width="11" height="11" rx="2" />
      <path d="M7 15H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h7a2 2 0 0 1 2 2v1" />
    </svg>
  );
}

function pendingUserSummary(event: MessageEvent): string | undefined {
  if (event.role !== "user" || event.pending !== true) {
    return undefined;
  }
  const state = String(event.request_state || "").trim().toLowerCase();
  if (state === "sending") {
    return "Sending";
  }
  if (state === "buffered") {
    return "Buffered";
  }
  return "Queued";
}

function ChatMessageCard({
  event,
  kind,
  options,
  turnMeta,
  commandOutput,
}: {
  event: MessageEvent;
  kind: "system" | "user" | "assistant";
  options: MarkdownRenderOptions;
  turnMeta?: AssistantTurnMeta;
  commandOutput?: boolean;
}) {
  const executedCommand = executedCommandText(event);
  const label = kind === "user" ? executedCommand ? "Command" : "You" : kind === "system" ? "System" : "Assistant";
  const text = contentTextFromMessage(event);
  const summary = kind === "user" ? pendingUserSummary(event) : undefined;
  const [copied, setCopied] = useState(false);
  const resetTimerRef = useRef<number | null>(null);

  useEffect(() => () => {
    if (resetTimerRef.current !== null) {
      window.clearTimeout(resetTimerRef.current);
    }
  }, []);

  const handleCopy = async () => {
    if (!await writeClipboardText(text)) {
      return;
    }
    setCopied(true);
    if (resetTimerRef.current !== null) {
      window.clearTimeout(resetTimerRef.current);
    }
    resetTimerRef.current = window.setTimeout(() => {
      setCopied(false);
      resetTimerRef.current = null;
    }, 1200);
  };

  return (
    <MessageSurface kind={kind}>
      {renderCardHeader(kind, label, summary, event.ts)}
      {executedCommand ? <CommandIOBlock label="Input" value={executedCommand} options={options} /> : null}
      {kind === "assistant" && commandOutput ? <CommandIOBlock label="Output" value={text} options={options} /> : executedCommand ? null : renderRichText(text, "messageBody", options)}
      {kind === "assistant" && turnMeta ? <AssistantTurnMetaCard meta={turnMeta} /> : null}
      {kind === "assistant" && Array.isArray(event.supervisor_runs) && event.supervisor_runs.length ? (
        <div className="supervisorRunStack" data-testid="supervisor-run-stack">
          {event.supervisor_runs.map((run, index) => <SupervisorRunCard key={run.run_id || `${run.anchor_assistant_event_id}-${index}`} run={run} />)}
        </div>
      ) : null}
      <div className="messageBubbleActions">
        <button
          type="button"
          className={cn("messageCopyButton", copied && "isCopied")}
          aria-label={`Copy ${kind} message`}
          onClick={() => {
            void handleCopy();
          }}
        >
          <CopyMessageIcon />
        </button>
      </div>
    </MessageSurface>
  );
}

function executedCommandText(event: MessageEvent): string {
  if (event.role !== "user") {
    return "";
  }
  const text = firstNonEmptyText(event.text);
  if (!text.startsWith("/")) {
    return "";
  }
  return text;
}

function CommandIOBlock({ label, value, options }: { label: "Input" | "Output"; value: string; options: MarkdownRenderOptions }) {
  if (!value.trim()) {
    return null;
  }
  return (
    <section className="commandIOBlock" data-testid={`command-${label.toLowerCase()}`}>
      <div className="commandIOLabel">{label}</div>
      {renderRichText(value, "messageBody commandIOBody", options)}
    </section>
  );
}

function SupervisorRunCard({ run }: { run: NonNullable<MessageEvent["supervisor_runs"]>[number] }) {
  const status = String(run.status || "").trim();
  const injectedText = firstNonEmptyText(run.injected_text);
  const reason = firstNonEmptyText(run.reason);
  const error = firstNonEmptyText(run.error);
  const title = status === "injected"
    ? `Supervisor injected: ${injectedText}`
    : status === "error"
      ? "Supervisor error"
      : "Supervisor stopped";
  return (
    <div className={cn("supervisorRunCard rounded-xl border border-border/60 bg-background/70 p-3 text-sm", status === "error" && "isError")} data-testid="supervisor-run-card">
      <div className="font-semibold text-foreground">{title}</div>
      {reason ? <div className="text-muted-foreground">Reason: {reason}</div> : null}
      {error ? <div className="text-destructive">Error: {error}</div> : null}
    </div>
  );
}

function pluralTurnMetric(count: number, singular: string, plural = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : plural}`;
}

function assistantTurnSummaryParts(meta: AssistantTurnMeta): { summary: string; details: string } {
  const turnLabel = `turn ${formatDuration(meta.turnSeconds)}`;
  const finishedLabel = `finished ${meta.finishedTs !== null ? formatTurnMetaTimestamp(meta.finishedTs) : "-"}`;

  if (meta.tools <= 0) {
    return {
      summary: turnLabel.charAt(0).toUpperCase() + turnLabel.slice(1),
      details: meta.errors > 0 ? `${pluralTurnMetric(meta.errors, "error")} · ${finishedLabel}` : finishedLabel,
    };
  }

  const summaryParts = [
    `Ran ${pluralTurnMetric(meta.tools, "tool")}`,
    `${meta.ok} ok`,
    meta.errors > 0 ? pluralTurnMetric(meta.errors, "error") : "",
  ].filter(Boolean);
  const detailParts = [
    `max tool ${formatDuration(meta.maxToolCallSeconds)}`,
    turnLabel,
    finishedLabel,
  ];
  return {
    summary: summaryParts.join(" · "),
    details: detailParts.join(" · "),
  };
}

function AssistantTurnMetaCard({ meta }: { meta: AssistantTurnMeta }) {
  const parts = assistantTurnSummaryParts(meta);
  return (
    <div
      className={cn("machineTraceSummaryRow assistantTurnSummaryRow", meta.errors > 0 && "isError")}
      data-testid="assistant-turn-meta"
      title={`${parts.summary} · ${parts.details}`}
    >
      <span className="machineTraceSummaryStatus" aria-hidden="true" />
      <span className="machineTraceSummaryMain">
        <span className="machineTraceSummaryText">{parts.summary}</span>
        <span className="machineTraceSummarySubtext">{parts.details}</span>
      </span>
      <span className="machineTraceSummaryMeta">Summary</span>
    </div>
  );
}

function MessageSurface({
  kind,
  children,
  grouped = false,
  isError = false,
  compact = false,
  className,
  contentClassName,
}: {
  kind: string;
  children: ComponentChildren;
  grouped?: boolean;
  isError?: boolean;
  compact?: boolean;
  className?: string;
  contentClassName?: string;
}) {
  const isChatSurface = kind === "system" || kind === "user" || kind === "assistant" || kind === "ask_user";

  return (
    <Card
      data-testid="message-surface"
      data-kind={kind}
      className={cn(
        "messageSurface rounded-[1.35rem] border shadow-sm backdrop-blur-sm transition-colors",
        isChatSurface ? "max-w-3xl" : compact ? "max-w-[56rem]" : "max-w-4xl",
        kind,
        kind === "system" ? "mr-auto messageBubble system" : undefined,
        kind === "user" ? "ml-auto messageBubble user" : undefined,
        kind === "assistant" ? "mr-auto messageBubble assistant" : undefined,
        kind === "ask_user" ? "mr-auto messageBubble messageCard ask_user" : undefined,
        !isChatSurface && !compact ? "messageCard" : undefined,
        compact ? "border-0 bg-transparent shadow-none backdrop-blur-none" : undefined,
        grouped && "grouped",
        isError && "isError",
        !compact ? messageSurfaceTone(kind, isError) : undefined,
        className,
      )}
    >
      <CardContent className={cn(compact ? "p-0" : "space-y-3 p-4", contentClassName)}>{children}</CardContent>
    </Card>
  );
}

function renderChatCard(event: MessageEvent, kind: "system" | "user" | "assistant", options: MarkdownRenderOptions, turnMeta?: AssistantTurnMeta, commandOutput?: boolean) {
  if (kind === "system") {
    return <SystemPromptCard event={event} options={options} />;
  }
  return <ChatMessageCard event={event} kind={kind} options={options} turnMeta={turnMeta} commandOutput={commandOutput} />;
}

function shouldAllowFuzzyAskUserMatch(messages: MessageEvent[], index: number) {
  const event = messages[index];
  if (!isUnresolvedAskUserEvent(event)) return true;
  const signature = askUserHistorySignature(event);
  if (!signature) return true;
  for (let cursor = index + 1; cursor < messages.length; cursor += 1) {
    const candidate = messages[cursor];
    if (!isUnresolvedAskUserEvent(candidate)) continue;
    if (askUserHistorySignature(candidate) === signature) {
      return false;
    }
  }
  return true;
}

function renderAskUserCard(
  event: MessageEvent,
  sessionId: string | undefined,
  runtimeId: string | null | undefined,
  options: MarkdownRenderOptions,
  allowFuzzyLiveMatch: boolean,
  allowLegacyFallback: boolean,
) {
  return (
    <AskUserCard
      event={event}
      sessionId={sessionId}
      runtimeId={runtimeId}
      allowFuzzyLiveMatch={allowFuzzyLiveMatch}
      allowLegacyFallback={allowLegacyFallback}
      renderRichText={(value, className) => renderRichText(value, className, options)}
    />
  );
}

function renderReasoningCard(event: MessageEvent, options: MarkdownRenderOptions) {
  const summary = firstNonEmptyText(event.summary);
  const body = firstNonEmptyText(event.text, summary);

  return (
    <MessageSurface kind="reasoning">
      {renderCardHeader("reasoning", undefined, summary && summary !== body ? summary : undefined, event.ts)}
      {body ? <ExpandableRichText key={body} value={body} options={options} /> : null}
    </MessageSurface>
  );
}

function ToolCallIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M14.5 6.5a4.5 4.5 0 0 0 3.9 6.64l-7.26 7.26a1.5 1.5 0 0 1-2.12 0l-.42-.42a1.5 1.5 0 0 1 0-2.12l7.26-7.26A4.5 4.5 0 0 1 10.5 5.5l2.07 2.07 1.93-1.07z" />
    </svg>
  );
}

function ProcessUpdateIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="4" y="5" width="12" height="14" rx="2" />
      <path d="M8 9h4" />
      <path d="M8 13h4" />
      <path d="M19 9v6" />
      <path d="m16.5 11.5 2.5-2.5 2.5 2.5" />
    </svg>
  );
}

function ToolResultIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="4" y="5" width="16" height="14" rx="2" />
      <path d="M8 10.5h8M8 14h5" />
    </svg>
  );
}

function TodoChangeIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M9 6h11" />
      <path d="M9 12h11" />
      <path d="M9 18h11" />
      <path d="m4.5 6 1.5 1.5L8.5 5" />
      <path d="m4.5 12 1.5 1.5L8.5 11" />
      <path d="m4.5 18 1.5 1.5L8.5 17" />
    </svg>
  );
}

function ReasoningIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M9.5 7.5A3.5 3.5 0 0 0 6 11c0 1.2.6 2.3 1.5 2.95.62.45 1 .96 1 1.55v1h7v-1c0-.59.38-1.1 1-1.55A3.6 3.6 0 0 0 18 11a3.5 3.5 0 0 0-3.5-3.5c-.93 0-1.78.36-2.42.95A3.47 3.47 0 0 0 9.5 7.5Z" />
      <path d="M10 16.5v2" />
      <path d="M14 16.5v2" />
      <path d="M9.5 20.5h5" />
      <path d="M9.75 11.25h.01" />
      <path d="M14.25 11.25h.01" />
      <path d="M10.75 13.5c.32.38.77.6 1.25.6s.93-.22 1.25-.6" />
    </svg>
  );
}

function EmptyOutputIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 4v9" />
      <path d="m8.5 9 3.5-5 3.5 5" />
      <path d="M5 18h14" />
      <path d="M8 21h8" />
    </svg>
  );
}

function TurnTerminalIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M7 5v14" />
      <path d="M7 6h10l-3 3 3 3H7" />
      <path d="M4 20h16" />
    </svg>
  );
}

function CompactionIcon({ phase }: { phase?: string | null }) {
  const ending = phase === "end";
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="5" y="5" width="14" height="14" rx="2" />
      <path d="M8 9h8" />
      <path d="M8 12h6" />
      <path d="M8 15h4" />
      {ending ? <path d="m16 13 2.5 2.5L21 13" /> : <path d="m16 11 2.5-2.5L21 11" />}
    </svg>
  );
}

function ExtensionUIIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="4" y="5" width="16" height="14" rx="2" />
      <path d="M8 9h8" />
      <path d="M8 13h5" />
      <path d="M16 15.5h.01" />
    </svg>
  );
}

function TerminalToolIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="4" y="5" width="16" height="14" rx="2" />
      <path d="m8 10 3 2-3 2" />
      <path d="M13 15h3" />
    </svg>
  );
}

function ReadToolIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M6 4h8l4 4v12H6z" />
      <path d="M14 4v5h4" />
      <path d="M9 13h6" />
      <path d="M9 16h4" />
    </svg>
  );
}

function WriteToolIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M6 4h8l4 4v12H6z" />
      <path d="M14 4v5h4" />
      <path d="m9 16 5.5-5.5 2 2L11 18H9z" />
    </svg>
  );
}

function SearchToolIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="10.5" cy="10.5" r="5.5" />
      <path d="m15 15 5 5" />
      <path d="M8 10h5" />
    </svg>
  );
}

function ContextToolIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="6" cy="12" r="2" />
      <circle cx="18" cy="6" r="2" />
      <circle cx="18" cy="18" r="2" />
      <path d="M8 12h4" />
      <path d="m13 11 3-4" />
      <path d="m13 13 3 4" />
    </svg>
  );
}

function normalizedToolName(event: MessageEvent): string {
  return firstNonEmptyText(event.name, typeof event.details?.name === "string" ? event.details.name : "").trim().toLowerCase();
}

function compactionDetails(event: MessageEvent): Record<string, unknown> | null {
  return asRecord(asRecord(event.details)?.compaction);
}

function compactionPhase(event: MessageEvent): string | null {
  const phase = compactionDetails(event)?.phase;
  if (typeof phase === "string") {
    return phase;
  }
  const text = firstNonEmptyText(event.summary, event.text).toLowerCase();
  if (text.includes("compaction started") || text.includes("compacting")) {
    return "start";
  }
  if (text.includes("compaction ended") || text.includes("compaction finished")) {
    return "end";
  }
  return null;
}

function machineTraceToolFamily(event: MessageEvent): string {
  const name = normalizedToolName(event);
  if (!name) {
    return "generic";
  }
  if (name === "bash" || name === "shell" || name === "exec" || name === "process") {
    return "terminal";
  }
  if (name === "read" || name === "file_read" || name === "open" || name === "glob") {
    return "read";
  }
  if (name === "write" || name === "edit" || name === "multi_edit" || name === "file_write") {
    return "write";
  }
  if (name === "grep" || name === "rg" || name === "find" || name === "search") {
    return "search";
  }
  if (name === "manage_todo_list" || name.includes("todo")) {
    return "todo";
  }
  if (name.startsWith("context_")) {
    return "context";
  }
  return "generic";
}

function machineTraceIcon(event: MessageEvent, kind: CompactTraceKind, piEventVariant: string | null) {
  if (kind === "tool" || kind === "tool_result") {
    switch (machineTraceToolFamily(event)) {
      case "terminal":
        return <TerminalToolIcon />;
      case "read":
        return <ReadToolIcon />;
      case "write":
        return <WriteToolIcon />;
      case "search":
        return <SearchToolIcon />;
      case "todo":
        return <TodoChangeIcon />;
      case "context":
        return <ContextToolIcon />;
      default:
        return kind === "tool_result" ? <ToolResultIcon /> : <ToolCallIcon />;
    }
  }
  if (kind === "todo_snapshot") {
    return <TodoChangeIcon />;
  }
  if (kind === "custom_message") {
    return <ProcessUpdateIcon />;
  }
  if (kind === "pi_event") {
    return piEventVariant === PI_EVENT_COMPACT_VARIANTS.extension_ui
      ? <ExtensionUIIcon />
      : piEventVariant === PI_EVENT_COMPACT_VARIANTS.compaction
        ? <CompactionIcon phase={compactionPhase(event)} />
        : piEventVariant === PI_EVENT_COMPACT_VARIANTS.turn_terminal
          ? <TurnTerminalIcon />
          : <EmptyOutputIcon />;
  }
  return <ReasoningIcon />;
}

function machineTraceTitle(event: MessageEvent, kind: CompactTraceKind) {
  if (kind === "reasoning") {
    return firstNonEmptyText(event.summary, "Reasoning");
  }
  if (kind === "tool") {
    return firstNonEmptyText(event.name, "Tool");
  }
  if (kind === "todo_snapshot") {
    return firstNonEmptyText(event.progress_text, event.operation, "Todo update");
  }
  if (kind === "custom_message") {
    return firstNonEmptyText(event.text, event.summary, event.custom_type, "Process update");
  }
  if (kind === "pi_event") {
    const compaction = compactionDetails(event);
    if (compaction) {
      const phase = typeof compaction.phase === "string" ? compaction.phase : "";
      if (phase === "start") return "Compaction started";
      if (phase === "end") return "Compaction ended";
    }
    return firstNonEmptyText(event.summary, event.text, "System event");
  }
  return firstNonEmptyText(event.name, event.summary, "Tool result");
}

function machineTraceSummary(event: MessageEvent, kind: CompactTraceKind) {
  if (kind === "reasoning") {
    return compactSingleLine(firstNonEmptyText(event.summary, event.text), 90);
  }
  if (kind === "tool") {
    return compactSingleLine(firstNonEmptyText(event.summary, event.text, event.context), 90);
  }
  if (kind === "todo_snapshot") {
    const items = Array.isArray(event.items)
      ? event.items
        .map((item) => firstNonEmptyText(item.title, item.description))
        .filter((value): value is string => Boolean(value))
      : [];
    return compactSingleLine(firstNonEmptyText(event.progress_text, event.operation, items.join(", ")), 90);
  }
  if (kind === "custom_message") {
    return compactSingleLine(firstNonEmptyText(event.summary, event.text, event.custom_type), 90);
  }
  if (kind === "pi_event") {
    const compaction = compactionDetails(event);
    if (compaction) {
      const reason = typeof compaction.reason === "string" ? compaction.reason : "";
      const model = asRecord(compaction.model);
      const modelID = typeof model?.id === "string" ? model.id : "";
      const inputTokensK = typeof compaction.inputTokensK === "number" ? `${compaction.inputTokensK.toFixed(1)}K` : "";
      const tokensAfterK = typeof compaction.tokensAfterK === "number" ? `${compaction.tokensAfterK.toFixed(1)}K` : "";
      const retry = compaction.willRetry === true ? "retrying" : "";
      const error = typeof compaction.errorMessage === "string" ? compaction.errorMessage : "";
      return compactSingleLine(firstNonEmptyText(error, [reason, inputTokensK, tokensAfterK, modelID, retry].filter(Boolean).join(" "), event.text), 90);
    }
    const detailsText = event.details ? JSON.stringify(event.details, null, 2) : "";
    return compactSingleLine(firstNonEmptyText(event.text, detailsSummary(event.details), detailsText), 90);
  }
  const detailsText = !event.text && event.details ? JSON.stringify(event.details, null, 2) : "";
  return compactSingleLine(firstNonEmptyText(event.summary, event.text, detailsSummary(event.details), detailsText), 90);
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function toolCallArgumentDetails(event: MessageEvent): Record<string, unknown> | null {
  const details = asRecord(event.details);
  if (!details) {
    return null;
  }
  return asRecord(details.arguments);
}

function toolCallRawArguments(event: MessageEvent): string | null {
  const details = asRecord(event.details);
  const raw = details?.raw_arguments;
  return typeof raw === "string" && raw.trim() ? raw.trim() : null;
}

function processDetails(event: MessageEvent): Record<string, unknown> | null {
  return asRecord(event.details);
}

function processNameFromDetails(details: Record<string, unknown> | null): string {
  return firstNonEmptyText(
    typeof details?.processName === "string" ? details.processName : "",
    typeof details?.name === "string" ? details.name : "",
    typeof asRecord(details?.process)?.name === "string" ? asRecord(details?.process)?.name as string : "",
    "process",
  );
}

function isBashTool(name: unknown): boolean {
  if (typeof name !== "string") {
    return false;
  }
  return name === "bash" || name.startsWith("bashExecution");
}

function isRawCodeToolOutput(name: unknown): boolean {
  if (typeof name !== "string") {
    return false;
  }
  if (isBashTool(name)) {
    return true;
  }
  return name === "read" || name === "grep" || name === "find" || name === "ls";
}

function todoStatusClass(status: unknown): string {
  if (typeof status !== "string") {
    return "unknown";
  }
  const normalized = status.trim().toLowerCase().replace(/_/g, "-");
  return normalized || "unknown";
}

function todoStatusLabel(status: unknown): string {
  if (typeof status !== "string") {
    return "unknown";
  }
  return status.trim().replace(/_/g, "-") || "unknown";
}

function normalizeTodoItems(todos: unknown[]): TodoSnapshotItem[] {
  return todos
    .map((item) => asRecord(item))
    .filter((item): item is Record<string, unknown> => Boolean(item))
    .map((item) => ({
      id: typeof item.id === "number" || typeof item.id === "string" ? item.id : undefined,
      title: typeof item.title === "string" ? item.title : undefined,
      description: typeof item.description === "string" ? item.description : undefined,
      status: todoStatusLabel(item.status),
    }));
}

function maybeParseJsonObject(value: string): unknown {
  const text = value.trim();
  if (!text || (text[0] !== "{" && text[0] !== "[")) {
    return null;
  }
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

function todoItemsFromUnknown(value: unknown): TodoSnapshotItem[] {
  if (Array.isArray(value)) {
    return normalizeTodoItems(value);
  }
  if (typeof value === "string") {
    return todoItemsFromUnknown(maybeParseJsonObject(value));
  }
  const record = asRecord(value);
  if (!record) {
    return [];
  }
  if (Array.isArray(record.todos)) {
    return normalizeTodoItems(record.todos);
  }
  if (typeof record.todos === "string") {
    const parsed = maybeParseJsonObject(record.todos);
    const fromTodos = todoItemsFromUnknown(parsed);
    if (fromTodos.length > 0) {
      return fromTodos;
    }
  }
  if (record.details) {
    const nested = todoItemsFromUnknown(record.details);
    if (nested.length > 0) {
      return nested;
    }
  }
  return [];
}

function todoItemsFromEvent(event: MessageEvent): TodoSnapshotItem[] {
  const fromDetails = todoItemsFromUnknown(event.details);
  if (fromDetails.length > 0) {
    return fromDetails;
  }
  return todoItemsFromUnknown(firstNonEmptyText(event.text));
}

function todoItemsFromText(value: string | null | undefined): TodoSnapshotItem[] {
  if (!value) {
    return [];
  }
  return todoItemsFromUnknown(value);
}

function renderCodeBlock(value: string) {
  return <pre className="messageCardPre overflow-x-auto rounded-xl bg-background/80 p-3 text-sm"><code>{value}</code></pre>;
}

function renderTodoItemsList(items: TodoSnapshotItem[]) {
  if (!items.length) {
    return null;
  }
  return (
    <ul className="messageTodoList space-y-2">
      {items.map((item, index) => (
        <li key={`${item.id ?? item.title ?? "todo"}-${index}`} className="messageTodoItem flex items-start gap-3 rounded-xl px-3 py-2 text-sm">
          <span className={cn("messageTodoStatus rounded-full px-2 py-0.5 text-xs font-semibold uppercase tracking-wide", todoStatusClass(item.status))}>{todoStatusLabel(item.status)}</span>
          <span>{item.title || item.description || "Untitled item"}</span>
        </li>
      ))}
    </ul>
  );
}

function stripTrailingPeriod(value: string): string {
  return value.endsWith(".") ? value.slice(0, -1) : value;
}

function parseWriteResult(text: string): { bytes: string; path: string } | null {
  const match = text.match(/^Successfully wrote\s+(\d+)\s+bytes\s+to\s+(.+)$/);
  if (!match) {
    return null;
  }
  return { bytes: match[1] || "", path: stripTrailingPeriod((match[2] || "").trim()) };
}

function parseEditResult(text: string, details: Record<string, unknown> | null): { path: string; blocks: string; firstChangedLine: string | null } | null {
  const match = text.match(/^Successfully replaced(?:\s+(\d+)\s+block\(s\)|\s+text)\s+in\s+(.+)$/);
  if (!match) {
    return null;
  }
  const firstChangedLine = typeof details?.firstChangedLine === "number" ? String(details.firstChangedLine) : null;
  return {
    path: stripTrailingPeriod((match[2] || "").trim()),
    blocks: (match[1] || "1").trim(),
    firstChangedLine,
  };
}

function parseContextTagResult(text: string): { tag: string; target: string } | null {
  const match = text.match(/^Created tag '([^']+)' at\s+([0-9a-fA-F]+)$/);
  if (!match) {
    return null;
  }
  return { tag: match[1] || "", target: match[2] || "" };
}

function parseContextLogDashboard(text: string): { usage: string; segment: string } | null {
  if (!text.startsWith("[Context Dashboard]")) {
    return null;
  }
  const usageLine = text.split("\n").find((line) => line.includes("Context Usage:"));
  const segmentLine = text.split("\n").find((line) => line.includes("Segment Size:"));
  if (!usageLine || !segmentLine) {
    return null;
  }
  const usage = usageLine.split("Context Usage:")[1]?.trim() || "";
  const segment = segmentLine.split("Segment Size:")[1]?.trim() || "";
  if (!usage || !segment) {
    return null;
  }
  return { usage, segment };
}

function parseContextCheckoutResult(text: string): { phase: string; note: string } | null {
  if (text.trim().toLowerCase() === "checkout start") {
    return { phase: "start", note: "Checkout procedure started" };
  }
  const match = text.match(/^Checked out to\s+(.+)$/i);
  if (match) {
    return { phase: "completed", note: (match[1] || "").trim() };
  }
  return null;
}

function renderStructuredToolCall(event: MessageEvent, args: Record<string, unknown> | null, rawArgs: string | null) {
  if (event.name === "bash" || (typeof event.name === "string" && event.name.startsWith("bashExecution"))) {
    const command = typeof args?.command === "string" ? args.command : rawArgs;
    const timeout = typeof args?.timeout === "number" ? String(args.timeout) : null;
    if (!command) {
      return null;
    }
    return (
      <div className="space-y-2">
        {timeout ? (
          <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
            <span className="block text-xs uppercase tracking-wide text-muted-foreground">Timeout</span>
            <strong>{timeout}</strong>
          </div>
        ) : null}
        {renderCodeBlock(command)}
      </div>
    );
  }

  if (event.name === "grep") {
    const pattern = typeof args?.pattern === "string" ? args.pattern : rawArgs;
    const path = typeof args?.path === "string" ? args.path : null;
    const glob = typeof args?.glob === "string" ? args.glob : null;
    const limit = typeof args?.limit === "number" ? String(args.limit) : null;
    const context = typeof args?.context === "number" ? String(args.context) : null;
    if (!pattern) {
      return null;
    }
    return (
      <div className="space-y-2">
        <div className="grid grid-cols-2 gap-2">
          {path ? (
            <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm col-span-2">
              <span className="block text-xs uppercase tracking-wide text-muted-foreground">Path</span>
              <strong>{path}</strong>
            </div>
          ) : null}
          {glob ? (
            <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
              <span className="block text-xs uppercase tracking-wide text-muted-foreground">Glob</span>
              <strong>{glob}</strong>
            </div>
          ) : null}
          {limit ? (
            <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
              <span className="block text-xs uppercase tracking-wide text-muted-foreground">Limit</span>
              <strong>{limit}</strong>
            </div>
          ) : null}
          {context ? (
            <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
              <span className="block text-xs uppercase tracking-wide text-muted-foreground">Context</span>
              <strong>{context}</strong>
            </div>
          ) : null}
        </div>
        {renderCodeBlock(pattern)}
      </div>
    );
  }

  if (event.name === "edit") {
    const path = typeof args?.path === "string" ? args.path : "";
    const hasEdits = Array.isArray(args?.edits);
    const mode = hasEdits ? "multi" : "single";
    const blocks = hasEdits ? String((args?.edits as unknown[]).length) : "1";
    if (!path) {
      return null;
    }
    return (
      <div className="grid grid-cols-2 gap-2">
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Mode</span>
          <strong>{mode}</strong>
        </div>
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Blocks</span>
          <strong>{blocks}</strong>
        </div>
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm col-span-2">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Path</span>
          <strong>{path}</strong>
        </div>
      </div>
    );
  }

  return null;
}

function renderStructuredToolResult(event: MessageEvent) {
  const text = firstNonEmptyText(event.text);
  if (!text) {
    return null;
  }
  const details = asRecord(event.details);

  if (event.name === "write") {
    const parsed = parseWriteResult(text);
    if (!parsed) {
      return null;
    }
    return (
      <div className="grid grid-cols-2 gap-2">
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Bytes</span>
          <strong>{parsed.bytes}</strong>
        </div>
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm col-span-2">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Path</span>
          <strong>{parsed.path}</strong>
        </div>
      </div>
    );
  }

  if (event.name === "edit") {
    const parsed = parseEditResult(text, details);
    if (!parsed) {
      return null;
    }
    return (
      <div className="grid grid-cols-2 gap-2">
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Blocks Changed</span>
          <strong>{parsed.blocks}</strong>
        </div>
        {parsed.firstChangedLine ? (
          <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
            <span className="block text-xs uppercase tracking-wide text-muted-foreground">First Changed Line</span>
            <strong>{parsed.firstChangedLine}</strong>
          </div>
        ) : null}
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm col-span-2">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Path</span>
          <strong>{parsed.path}</strong>
        </div>
      </div>
    );
  }

  if (event.name === "context_tag") {
    const parsed = parseContextTagResult(text);
    if (!parsed) {
      return null;
    }
    return (
      <div className="grid grid-cols-2 gap-2">
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Tag</span>
          <strong>{parsed.tag}</strong>
        </div>
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Target</span>
          <strong>{parsed.target}</strong>
        </div>
      </div>
    );
  }

  if (event.name === "context_log") {
    const parsed = parseContextLogDashboard(text);
    if (!parsed) {
      return null;
    }
    return (
      <div className="grid grid-cols-1 gap-2">
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Context Usage</span>
          <strong>{parsed.usage}</strong>
        </div>
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Segment Size</span>
          <strong>{parsed.segment}</strong>
        </div>
      </div>
    );
  }

  if (event.name === "context_checkout") {
    const parsed = parseContextCheckoutResult(text);
    if (!parsed) {
      return null;
    }
    return (
      <div className="grid grid-cols-2 gap-2">
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Checkout</span>
          <strong>{parsed.phase}</strong>
        </div>
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Note</span>
          <strong>{parsed.note}</strong>
        </div>
      </div>
    );
  }

  return null;
}

function toolCallID(event: MessageEvent): string {
  if (typeof event.tool_call_id === "string" && event.tool_call_id.trim()) {
    return event.tool_call_id.trim();
  }
  const details = event.details && typeof event.details === "object" ? event.details as Record<string, unknown> : null;
  return typeof details?.tool_call_id === "string" ? details.tool_call_id.trim() : "";
}

function isTurnErrorOperation(event: MessageEvent): boolean {
  const kind = eventKind(event);
  return kind === "error" || (kind === "tool_result" && event.is_error === true);
}

function commandOutputByAssistantIndex(messages: MessageEvent[]): Set<number> {
  const result = new Set<number>();
  let waitingForCommandOutput = false;
  for (let index = 0; index < messages.length; index += 1) {
    const event = messages[index];
    if (event.role === "user") {
      waitingForCommandOutput = Boolean(executedCommandText(event));
      continue;
    }
    if (waitingForCommandOutput && event.role === "assistant" && event.streaming !== true) {
      result.add(index);
      waitingForCommandOutput = false;
    }
  }
  return result;
}

function localToolRunStats(segment: MessageEvent[]): Pick<AssistantTurnMeta, "tools" | "ok" | "errors"> {
  const toolCalls = segment.filter((item) => eventKind(item) === "tool").length;
  const toolResults = segment.filter((item) => eventKind(item) === "tool_result");
  return {
    tools: Math.max(toolCalls, toolResults.length),
    ok: toolResults.filter((item) => item.is_error !== true).length,
    errors: segment.filter(isTurnErrorOperation).length,
  };
}

function buildAssistantTurnMeta(messages: MessageEvent[]): Map<number, AssistantTurnMeta> {
  const result = new Map<number, AssistantTurnMeta>();
  let lastUserIndex = -1;
  for (let index = 0; index < messages.length; index += 1) {
    const event = messages[index];
    if (event.role === "user") {
      lastUserIndex = index;
      continue;
    }
    if (event.role !== "assistant" || event.streaming === true) {
      continue;
    }
    const start = lastUserIndex + 1;
    const segment = messages.slice(start, index);
    const assistantTs = eventTimestampSeconds(event);
    const userTs = lastUserIndex >= 0 ? eventTimestampSeconds(messages[lastUserIndex]) : null;
    const toolStarts = new Map<string, number>();
    let fallbackToolStart: number | null = null;
    let maxToolCallSeconds: number | null = null;
    for (const item of segment) {
      const kind = eventKind(item);
      const ts = eventTimestampSeconds(item);
      if (kind === "tool" && ts !== null) {
        const id = toolCallID(item);
        if (id) {
          toolStarts.set(id, ts);
        } else {
          fallbackToolStart = ts;
        }
      }
      if (kind === "tool_result" && ts !== null) {
        const id = toolCallID(item);
        const startTs = id ? toolStarts.get(id) : fallbackToolStart;
        if (startTs !== undefined && startTs !== null) {
          const elapsed = Math.max(0, ts - startTs);
          maxToolCallSeconds = maxToolCallSeconds === null ? elapsed : Math.max(maxToolCallSeconds, elapsed);
        }
      }
    }
    const localStats = localToolRunStats(segment);
    const backendSummary = backendToolActivitySummary(event);
    result.set(index, {
      tools: backendSummary?.total_tools ?? localStats.tools,
      ok: backendSummary?.ok ?? localStats.ok,
      errors: backendSummary?.failed ?? localStats.errors,
      maxToolCallSeconds: backendSummary?.max_tool_call_seconds ?? maxToolCallSeconds,
      turnSeconds: assistantTs !== null && userTs !== null ? Math.max(0, assistantTs - userTs) : null,
      finishedTs: assistantTs,
    });
  }
  return result;
}

function numberFromRecord(record: Record<string, unknown>, key: string): number {
  const value = record[key];
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function backendToolActivitySummary(event: MessageEvent): BackendToolActivitySummary | null {
  const details = asRecord(event.details);
  const raw = asRecord(details?.tool_activity_summary);
  if (!raw) {
    return null;
  }
  const totalTools = numberFromRecord(raw, "total_tools");
  const operations = numberFromRecord(raw, "operations");
  if (totalTools <= 0 && operations <= 0) {
    return null;
  }
  const runningNames = Array.isArray(raw.running_tool_names)
    ? raw.running_tool_names.filter((item): item is string => typeof item === "string" && item.trim().length > 0)
    : [];
  return {
    operations,
    total_tools: totalTools,
    tool_calls: numberFromRecord(raw, "tool_calls"),
    tool_results: numberFromRecord(raw, "tool_results"),
    running: numberFromRecord(raw, "running"),
    ok: numberFromRecord(raw, "ok"),
    failed: numberFromRecord(raw, "failed"),
    reasoning: numberFromRecord(raw, "reasoning"),
    todo_snapshots: numberFromRecord(raw, "todo_snapshots"),
    process_updates: numberFromRecord(raw, "process_updates"),
    system_events: numberFromRecord(raw, "system_events"),
    started_at: numberFromRecord(raw, "started_at") || undefined,
    last_activity_at: numberFromRecord(raw, "last_activity_at") || undefined,
    elapsed_seconds: numberFromRecord(raw, "elapsed_seconds") || undefined,
    max_tool_call_seconds: numberFromRecord(raw, "max_tool_call_seconds") || undefined,
    running_tool_names: runningNames,
    summary_text: typeof raw.summary_text === "string" ? raw.summary_text : undefined,
    status_text: typeof raw.status_text === "string" ? raw.status_text : undefined,
  };
}

function buildToolTracePairs(events: MessageEvent[]): Map<number, ToolTracePairInfo> {
  const calls = new Map<string, number[]>();
  const results = new Map<string, number[]>();

  for (let index = 0; index < events.length; index += 1) {
    const kind = eventKind(events[index]);
    if (kind !== "tool" && kind !== "tool_result") {
      continue;
    }
    const id = toolCallID(events[index]);
    if (!id) {
      continue;
    }
    const bucket = kind === "tool" ? calls : results;
    bucket.set(id, [...bucket.get(id) ?? [], index]);
  }

  const pairs = new Map<number, ToolTracePairInfo>();
  for (const [id, callIndexes] of calls) {
    const resultIndexes = results.get(id) ?? [];
    for (const index of callIndexes) {
      pairs.set(index, { id, state: resultIndexes.length > 0 ? "call" : "orphan" });
    }
    for (const index of resultIndexes) {
      pairs.set(index, { id, state: "result" });
    }
  }
  for (const [id, resultIndexes] of results) {
    if (calls.has(id)) {
      continue;
    }
    for (const index of resultIndexes) {
      pairs.set(index, { id, state: "orphan" });
    }
  }
  return pairs;
}

function hasToolResultAfter(events: MessageEvent[], tool: MessageEvent, startIndex: number): boolean {
  const callID = toolCallID(tool);
  for (let index = startIndex + 1; index < events.length; index += 1) {
    const event = events[index];
    if (eventKind(event) !== "tool_result") {
      continue;
    }
    if (!callID || toolCallID(event) === callID) {
      return true;
    }
  }
  return false;
}

function unresolvedToolRuntimeSeconds(events: MessageEvent[], index: number, nowSeconds: number): number | null {
  const event = events[index];
  if (!event || eventKind(event) !== "tool" || hasToolResultAfter(events, event, index)) {
    return null;
  }
  const ts = eventTimestampSeconds(event);
  if (ts === null) {
    return null;
  }
  return Math.max(0, Math.floor(nowSeconds - ts));
}

function formatRuntime(seconds: number) {
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m`;
  }
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

function formatRuntimePrecise(seconds: number | null) {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) {
    return "";
  }
  const total = Math.max(0, Math.floor(seconds));
  if (total < 60) {
    return `${total}s`;
  }
  const minutes = Math.floor(total / 60);
  const secs = total % 60;
  if (minutes < 60) {
    return secs ? `${minutes}m${secs}s` : `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  const remMinutes = minutes % 60;
  return remMinutes ? `${hours}h${remMinutes}m` : `${hours}h`;
}

function hasTrailingUnresolvedTool(events: MessageEvent[]) {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const kind = eventKind(events[index]);
    if (kind === "tool_result" || kind === "todo_snapshot") {
      return false;
    }
    if (kind === "tool") {
      return true;
    }
  }
  return false;
}

function machineTraceRunningIndex(events: MessageEvent[], isBusy: boolean) {
  if (!isBusy || !hasTrailingUnresolvedTool(events)) {
    return -1;
  }
  for (let index = events.length - 1; index >= 0; index -= 1) {
    if (eventKind(events[index]) === "tool") {
      return index;
    }
  }
  return -1;
}

function renderMachineTraceDetail(event: MessageEvent, kind: CompactTraceKind, options: MarkdownRenderOptions) {
  if (kind === "reasoning") {
    const summary = firstNonEmptyText(event.summary);
    const body = firstNonEmptyText(event.text, summary);
    return (
      <div className="machineTraceDetailBody space-y-3">
        {renderCardHeader("reasoning", undefined, summary && summary !== body ? summary : undefined, event.ts)}
        {body ? <ExpandableRichText key={body} value={body} options={options} /> : null}
      </div>
    );
  }

  if (kind === "tool") {
    const body = firstNonEmptyText(event.text, event.summary, event.context);
    const args = toolCallArgumentDetails(event);
    const rawArgs = toolCallRawArguments(event);
    const argsText = args ? JSON.stringify(args, null, 2) : rawArgs;
    const structuredCall = renderStructuredToolCall(event, args, rawArgs);
    const isProcessTool = event.name === "process";
    const isRawCode = isRawCodeToolOutput(event.name);
    return (
      <div className={cn("machineTraceDetailBody space-y-3", isProcessTool && "processToolDetail")}> 
        {renderCardHeader("tool", machineTraceTitle(event, kind), event.summary || undefined, event.ts)}
        {isProcessTool && args ? (
          <div className="grid grid-cols-2 gap-2">
            {typeof args.action === "string" ? (
              <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
                <span className="block text-xs uppercase tracking-wide text-muted-foreground">Action</span>
                <strong>{args.action}</strong>
              </div>
            ) : null}
            {typeof args.name === "string" ? (
              <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
                <span className="block text-xs uppercase tracking-wide text-muted-foreground">Process Name</span>
                <strong>{args.name}</strong>
              </div>
            ) : null}
            {typeof args.id === "string" ? (
              <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm col-span-2">
                <span className="block text-xs uppercase tracking-wide text-muted-foreground">Process ID</span>
                <strong>{args.id}</strong>
              </div>
            ) : null}
          </div>
        ) : null}
        {structuredCall}
        {body ? (isRawCode ? renderCodeBlock(body) : renderRichText(body, "messageBody", options)) : null}
        {!structuredCall && argsText ? renderCodeBlock(argsText) : null}
        {!structuredCall && !argsText ? <div className="messageCardFooterText text-sm text-muted-foreground">No additional tool input.</div> : null}
      </div>
    );
  }

  if (kind === "todo_snapshot") {
    const items = Array.isArray(event.items) ? event.items : [];
    return (
      <div className="machineTraceDetailBody space-y-3">
        {renderCardHeader("todo_snapshot", machineTraceTitle(event, kind), firstNonEmptyText(event.operation), event.ts)}
        {items.length ? (
          <ul className="messageTodoList space-y-2">
            {items.map((item, index) => (
              <li key={`${item.title || "todo"}-${index}`} className="messageTodoItem flex items-start gap-3 rounded-xl px-3 py-2 text-sm">
                <span className={cn("messageTodoStatus rounded-full px-2 py-0.5 text-xs font-semibold uppercase tracking-wide", typeof item.status === "string" ? item.status : "unknown")}>{item.status || "unknown"}</span>
                <span>{item.title || item.description || "Untitled item"}</span>
              </li>
            ))}
          </ul>
        ) : <div className="messageCardFooterText text-sm text-muted-foreground">No todo items in this snapshot.</div>}
        {event.text ? renderRichText(event.text, "messageBody", options) : null}
      </div>
    );
  }

  if (kind === "custom_message") {
    const body = firstNonEmptyText(event.description, event.summary, event.text);
    const details = processDetails(event);
    const detailsText = event.details ? JSON.stringify(event.details, null, 2) : "";
    const isProcessUpdate = typeof event.custom_type === "string" && event.custom_type.startsWith("ad-process:");
    return (
      <div className={cn("machineTraceDetailBody space-y-3", isProcessUpdate && "processToolDetail")}> 
        {renderCardHeader("custom_message", machineTraceTitle(event, kind), typeof event.custom_type === "string" ? event.custom_type : undefined, event.ts)}
        {isProcessUpdate ? (
          <div className="grid grid-cols-2 gap-2">
            <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
              <span className="block text-xs uppercase tracking-wide text-muted-foreground">Process</span>
              <strong>{processNameFromDetails(details)}</strong>
            </div>
            {typeof details?.status === "string" ? (
              <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
                <span className="block text-xs uppercase tracking-wide text-muted-foreground">Status</span>
                <strong>{details.status}</strong>
              </div>
            ) : null}
            {typeof details?.exitCode === "number" ? (
              <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
                <span className="block text-xs uppercase tracking-wide text-muted-foreground">Exit Code</span>
                <strong>{details.exitCode}</strong>
              </div>
            ) : null}
            {typeof details?.runtime === "string" ? (
              <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
                <span className="block text-xs uppercase tracking-wide text-muted-foreground">Runtime</span>
                <strong>{details.runtime}</strong>
              </div>
            ) : null}
          </div>
        ) : null}
        {body ? renderRichText(body, "messageBody", options) : null}
        {detailsText ? <pre className="messageCardPre overflow-x-auto rounded-xl bg-background/80 p-3 text-sm">{detailsText}</pre> : null}
      </div>
    );
  }

  if (kind === "pi_event") {
    const compaction = compactionDetails(event);
    const body = firstNonEmptyText(event.text, detailsSummary(event.details));
    const detailsText = event.details ? JSON.stringify(event.details, null, 2) : "";
    return (
      <div className="machineTraceDetailBody space-y-3">
        {renderCardHeader("pi_event", machineTraceTitle(event, kind), undefined, event.ts)}
        {compaction ? <CompactionDetail compaction={compaction} /> : null}
        {body ? renderRichText(body, "messageBody", options) : null}
        {detailsText ? <pre className="messageCardPre overflow-x-auto rounded-xl bg-background/80 p-3 text-sm">{detailsText}</pre> : null}
      </div>
    );
  }

  const body = firstNonEmptyText(event.text, detailsSummary(event.details));
  const detailsText = !event.text && event.details ? JSON.stringify(event.details, null, 2) : "";
  const details = processDetails(event);
  const process = asRecord(details?.process);
  const todoItems = todoItemsFromEvent(event);
  const structured = renderStructuredToolResult(event);
  const isProcessResult = event.name === "process";
  const isRawCode = isRawCodeToolOutput(event.name);
  const todoItemsFromBody = todoItemsFromText(body);
  const isTodoToolResult = todoItems.length > 0;
  const hideTodoJsonBody = isTodoToolResult && todoItemsFromBody.length > 0;
  return (
    <div className={cn("machineTraceDetailBody space-y-3", isProcessResult && "processToolDetail")}> 
      {renderCardHeader("tool_result", machineTraceTitle(event, kind), event.summary || undefined, event.ts)}
      {isProcessResult ? (
        <div className="grid grid-cols-2 gap-2">
          <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
            <span className="block text-xs uppercase tracking-wide text-muted-foreground">Action</span>
            <strong>{typeof details?.action === "string" ? details.action : "output"}</strong>
          </div>
          <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
            <span className="block text-xs uppercase tracking-wide text-muted-foreground">Status</span>
            <strong>{typeof process?.status === "string" ? process.status : (typeof details?.success === "boolean" ? (details.success ? "ok" : "failed") : "unknown")}</strong>
          </div>
          <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
            <span className="block text-xs uppercase tracking-wide text-muted-foreground">Process</span>
            <strong>{processNameFromDetails(details)}</strong>
          </div>
          {typeof process?.id === "string" ? (
            <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
              <span className="block text-xs uppercase tracking-wide text-muted-foreground">Process ID</span>
              <strong>{process.id}</strong>
            </div>
          ) : null}
        </div>
      ) : null}
      {structured}
      {isTodoToolResult ? renderTodoItemsList(todoItems) : null}
      {body && !hideTodoJsonBody ? (isRawCode ? renderCodeBlock(body) : renderRichText(body, "messageBody", options)) : null}
      {!isTodoToolResult && !structured && detailsText ? renderCodeBlock(detailsText) : null}
    </div>
  );
}

function CompactionDetail({ compaction }: { compaction: Record<string, unknown> }) {
  const model = asRecord(compaction.model);
  const result = asRecord(compaction.result);
  const rows = [
    ["Phase", typeof compaction.phase === "string" ? compaction.phase : ""],
    ["Reason", typeof compaction.reason === "string" ? compaction.reason : ""],
    ["Input", typeof compaction.inputTokensK === "number" ? `${compaction.inputTokensK.toFixed(1)}K` : ""],
    ["Before", typeof compaction.tokensBefore === "number" ? String(compaction.tokensBefore) : ""],
    ["After", typeof compaction.tokensAfterK === "number" ? `${compaction.tokensAfterK.toFixed(1)}K` : ""],
    ["Model", [typeof model?.provider === "string" ? model.provider : "", typeof model?.id === "string" ? model.id : ""].filter(Boolean).join(" / ")],
    ["Duration", typeof compaction.durationMs === "number" ? `${compaction.durationMs} ms` : ""],
    ["Retry", compaction.willRetry === true ? "yes" : ""],
    ["Aborted", compaction.aborted === true ? "yes" : ""],
    ["Error", typeof compaction.errorMessage === "string" ? compaction.errorMessage : ""],
    ["First kept", typeof result?.firstKeptEntryId === "string" ? result.firstKeptEntryId : ""],
  ].filter(([, value]) => value);
  return (
    <div className="grid grid-cols-2 gap-2">
      {rows.map(([label, value]) => (
        <div key={label} className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">{label}</span>
          <strong>{value}</strong>
        </div>
      ))}
    </div>
  );
}

async function hydrateDeferredToolEvent(event: MessageEvent) {
  const sessionId = typeof event.session_id === "string" ? event.session_id : "";
  const seq = typeof event.seq === "number" ? event.seq : 0;
  const eventId = typeof event.event_id === "string" ? event.event_id : "";
  const toolCallId = typeof event.tool_call_id === "string" ? event.tool_call_id : "";
  if (!sessionId || (!seq && !eventId && !toolCallId)) {
    return event;
  }
  const page = eventId || toolCallId
    ? await api.listMessages(sessionId, true, undefined, undefined, undefined, 1, undefined, false, undefined, 0, true, eventId, toolCallId)
    : await api.listMessages(sessionId, true, undefined, undefined, seq + 1, 1, undefined, false);
  const items = Array.isArray(page.items) ? page.items : Array.isArray(page.events) ? page.events : [];
  return items.find((item) => (eventId && item.event_id === eventId) || (toolCallId && item.tool_call_id === toolCallId) || item.seq === seq) ?? event;
}

function MachineTraceSummaryRow({
  summary,
  expanded,
  onToggle,
  metaLabel,
}: {
  summary: ReturnType<typeof buildToolActivitySummary>;
  expanded: boolean;
  onToggle: () => void;
  metaLabel?: string;
}) {
  const runningLabel = summary.runningToolNames.length ? summary.runningToolNames.join(", ") : "";
  return (
    <button
      type="button"
      className={cn(
        "machineTraceSummaryRow",
        summary.running > 0 && "isRunning",
        summary.failed > 0 && "isError",
        summary.stalled && "isStalled",
      )}
      data-testid="machine-trace-summary"
      aria-expanded={expanded ? "true" : "false"}
      title={summary.summaryText}
      onClick={onToggle}
    >
      <span className="machineTraceSummaryStatus" aria-hidden="true" />
      <span className="machineTraceSummaryMain">
        <span className="machineTraceSummaryText">{summary.summaryText}</span>
        <span className="machineTraceSummarySubtext">
          {summary.stalled && summary.lastActivityAgeSeconds !== null
            ? `No output for ${formatRuntimePrecise(summary.lastActivityAgeSeconds)}`
            : summary.running > 0
              ? `Running${runningLabel ? `: ${runningLabel}` : ""}`
              : summary.statusText}
        </span>
      </span>
      <span className="machineTraceSummaryMeta">
        {metaLabel ?? (summary.hiddenEventCount > 0 && expanded ? `showing ${summary.visibleEvents.length}/${summary.visibleEvents.length + summary.hiddenEventCount}` : expanded ? "Hide" : "Details")}
      </span>
    </button>
  );
}

function MachineTraceToken({
  item,
  selected,
  running,
  runtimeSeconds,
  toolPair,
  onSelect,
}: {
  item: ToolActivityEvent;
  selected: boolean;
  running: boolean;
  runtimeSeconds: number | null;
  toolPair?: ToolTracePairInfo;
  onSelect: () => void;
}) {
  const { event, kind, key } = item;
  const piEventVariant = kind === "pi_event" ? piEventCompactVariant(event) : null;
  const summary = machineTraceSummary(event, kind);
  const tokenSummary = kind === "tool_result" && (todoItemsFromEvent(event).length > 0 || summary.includes('"todos"'))
    ? ""
    : summary;
  const title = machineTraceTitle(event, kind);
  const toolFamily = kind === "tool" || kind === "tool_result" ? machineTraceToolFamily(event) : undefined;
  const pairLabel = toolPair
    ? toolPair.state === "call"
      ? `paired call ${toolPair.id}`
      : toolPair.state === "result"
        ? `paired result ${toolPair.id}`
        : `unpaired tool ${toolPair.id}`
    : "";
  const runtimeLabel = typeof runtimeSeconds === "number" ? `running ${formatRuntime(runtimeSeconds)}` : "";
  const hiddenCount = item.priority >= 100 ? "important" : "";
  const accessibleLabel = [tokenSummary ? `${title}: ${tokenSummary}` : title, pairLabel, runtimeLabel, hiddenCount].filter(Boolean).join("; ");
  const statusLabel = kind === "tool_result"
    ? (event.is_error ? "error" : "complete")
    : kind === "todo_snapshot"
      ? "updated"
      : kind === "pi_event"
        ? (piEventVariant || "system")
        : running ? "running" : kind;
  return (
    <button
      key={`${kind}-${key}`}
      type="button"
      data-kind={kind}
      data-status={statusLabel}
      data-variant={piEventVariant || undefined}
      data-tool={toolFamily}
      data-pair-state={toolPair?.state}
      data-pair-id={toolPair?.id}
      className={cn(
        "machineTraceToken",
        kind,
        selected && "isSelected",
        running && "isRunning",
        event.is_error && "isError",
        toolPair?.state === "call" && "isPairedToolCall",
        toolPair?.state === "result" && "isPairedToolResult",
        toolPair?.state === "orphan" && "isUnpairedTool",
        (kind === "tool" || kind === "tool_result") && event.name === "process" && "isProcessTool",
        (piEventVariant === PI_EVENT_COMPACT_VARIANTS.turn_terminal || piEventVariant === PI_EVENT_COMPACT_VARIANTS.empty_output || piEventVariant === PI_EVENT_COMPACT_VARIANTS.retry_error) && "isAlert",
        piEventVariant === PI_EVENT_COMPACT_VARIANTS.extension_ui && "isExtensionUI",
        piEventVariant === PI_EVENT_COMPACT_VARIANTS.turn_terminal && "isTurnTerminal",
        piEventVariant === PI_EVENT_COMPACT_VARIANTS.compaction && "isCompaction",
      )}
      aria-expanded={selected ? "true" : "false"}
      title={accessibleLabel}
      aria-label={accessibleLabel}
      onClick={onSelect}
    >
      <span className="machineTraceTokenIcon" aria-hidden="true">
        {machineTraceIcon(event, kind, piEventVariant)}
      </span>
      {running ? <span className="machineTraceTokenPulse" aria-hidden="true" /> : null}
    </button>
  );
}

function CompactMachineTrace({ events, options, isBusy }: { events: MessageEvent[]; options: MarkdownRenderOptions; isBusy: boolean }) {
  const traceEvents = sortMachineTraceEvents(events);
  const runningIndex = machineTraceRunningIndex(traceEvents, isBusy);
  const toolTracePairs = buildToolTracePairs(traceEvents);
  const [expanded, setExpanded] = useState(false);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [hydratedByKey, setHydratedByKey] = useState<Record<string, MessageEvent>>({});
  const [nowSeconds, setNowSeconds] = useState(() => Date.now() / 1000);
  const activity = useMemo(() => buildToolActivitySummary(traceEvents, {
    nowSeconds,
    isBusy,
    visibleLimit: MACHINE_TRACE_VISIBLE_LIMIT,
    kindForEvent: machineTraceKindForActivity,
    eventKey: (event, kind, index) => eventStableIdentity(event, kind, index),
    eventTimestampSeconds,
    toolCallID,
    toolName: normalizedToolName,
    piEventVariant: piEventCompactVariant,
  }), [isBusy, nowSeconds, traceEvents]);
  const visibleEvents = expanded ? activity.visibleEvents : [];
  const selectedEvent = selectedKey == null
    ? null
    : hydratedByKey[selectedKey] ?? visibleEvents.find((item) => item.key === selectedKey)?.event ?? traceEvents.find((event, index) => {
        const kind = compactTraceKind(event);
        return kind && eventStableIdentity(event, kind, index) === selectedKey;
      }) ?? null;
  const selectedKind = selectedEvent ? compactTraceKind(selectedEvent) : null;
  const selectedVariant = selectedEvent ? piEventCompactVariant(selectedEvent) : null;

  useEffect(() => {
    if (!isBusy) {
      return undefined;
    }
    const interval = window.setInterval(() => setNowSeconds(Date.now() / 1000), 30_000);
    return () => window.clearInterval(interval);
  }, [isBusy]);

  useEffect(() => {
    if (!selectedKey || hydratedByKey[selectedKey]) {
      return undefined;
    }
    const event = traceEvents.find((candidate, index) => {
      const kind = compactTraceKind(candidate);
      return kind && eventStableIdentity(candidate, kind, index) === selectedKey;
    });
    const kind = event ? compactTraceKind(event) : null;
    if (!event || (kind !== "tool" && kind !== "tool_result") || event.text) {
      return undefined;
    }
    let cancelled = false;
    hydrateDeferredToolEvent(event).then((hydrated) => {
      if (!cancelled) {
        setHydratedByKey((current) => ({ ...current, [selectedKey]: hydrated }));
      }
    }).catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [hydratedByKey, selectedKey, traceEvents]);

  useEffect(() => {
    if (!expanded) {
      setSelectedKey(null);
    }
  }, [expanded]);

  return (
    <MessageSurface kind="event" compact className="machineTraceSurface" contentClassName="space-y-3">
      <MachineTraceSummaryRow summary={activity} expanded={expanded} onToggle={() => setExpanded((current) => !current)} />
      {expanded ? (
        <div className="machineTraceStrip" data-testid="machine-trace-strip">
          {visibleEvents.map((item) => (
            <MachineTraceToken
              key={`${item.kind}-${item.key}`}
              item={item}
              selected={selectedKey === item.key}
              running={item.index === runningIndex}
              runtimeSeconds={item.kind === "tool" ? unresolvedToolRuntimeSeconds(traceEvents, item.index, nowSeconds) : null}
              toolPair={toolTracePairs.get(item.index)}
              onSelect={() => setSelectedKey((current) => (current === item.key ? null : item.key))}
            />
          ))}
          {activity.hiddenEventCount > 0 ? (
            <div className="machineTraceHiddenCount" data-testid="machine-trace-hidden-count">
              +{activity.hiddenEventCount}
            </div>
          ) : null}
        </div>
      ) : null}
      {selectedEvent && selectedKind && isMachineTraceKind(selectedKind) ? (
        <div
          className={cn(
            "machineTraceDetail",
            selectedKind,
            selectedEvent.is_error && "isError",
            (selectedKind === "tool" || selectedKind === "tool_result") && selectedEvent.name === "process" && "isProcessTool",
            (selectedVariant === PI_EVENT_COMPACT_VARIANTS.turn_terminal || selectedVariant === PI_EVENT_COMPACT_VARIANTS.empty_output || selectedVariant === PI_EVENT_COMPACT_VARIANTS.retry_error) && "isAlert",
            selectedVariant === PI_EVENT_COMPACT_VARIANTS.extension_ui && "isExtensionUI",
            selectedVariant === PI_EVENT_COMPACT_VARIANTS.turn_terminal && "isTurnTerminal",
            selectedVariant === PI_EVENT_COMPACT_VARIANTS.compaction && "isCompaction",
          )}
          data-testid="machine-trace-detail"
        >
          {renderMachineTraceDetail(selectedEvent, selectedKind, options)}
        </div>
      ) : null}
    </MessageSurface>
  );
}

function TeamIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 4.5 17.5 8v6.5L12 18l-5.5-3.5V8L12 4.5Z" />
      <path d="M12 11.5v6.5" />
      <path d="M6.75 8.25 12 11.5l5.25-3.25" />
      <circle cx="8" cy="19" r="1.5" />
      <circle cx="16" cy="19" r="1.5" />
      <path d="M9.5 19h5" />
    </svg>
  );
}

function renderTeamCard(event: MessageEvent, options: MarkdownRenderOptions) {
  const output = firstNonEmptyText(event.output, event.text);
  const pending = !output;

  return (
    <MessageSurface kind="team">
      <div className="teamCardHeading flex items-start gap-3">
        <div className="teamCardIcon" data-testid="team-icon" aria-label="Team">
          <TeamIcon />
        </div>
        <div className="min-w-0 flex-1">
          {renderCardHeader("team", firstNonEmptyText(event.agent, "team"), firstNonEmptyText(event.task), event.ts)}
        </div>
      </div>
      <div className="messageMetaList flex flex-col gap-2">
        <div className="grid grid-cols-2 gap-2">
          {event.agent ? (
            <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
              <span className="block text-xs uppercase tracking-wide text-muted-foreground">Agent</span>
              <strong>{event.agent}</strong>
            </div>
          ) : null}
          <div className={cn("messageMetaItem rounded-xl bg-background/70 p-3 text-sm", !event.agent && "col-span-2")}>
            <span className="block text-xs uppercase tracking-wide text-muted-foreground">Status</span>
            <strong>{pending ? "Running" : "Completed"}</strong>
          </div>
        </div>
        {event.task ? (
          <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
            <span className="block text-xs uppercase tracking-wide text-muted-foreground">Task</span>
            <strong>{event.task}</strong>
          </div>
        ) : null}
      </div>
      {output ? renderRichText(output, "messageBody", options) : <div className="messageCardFooterText text-sm text-muted-foreground">Waiting for team output...</div>}
    </MessageSurface>
  );
}

function renderTodoSnapshotCard(event: MessageEvent, options: MarkdownRenderOptions) {
  const items = Array.isArray(event.items) ? event.items.slice(0, 3) : [];

  return (
    <MessageSurface kind="todo_snapshot">
      {renderCardHeader("todo_snapshot", firstNonEmptyText(event.progress_text, "Todo snapshot"), firstNonEmptyText(event.operation), event.ts)}
      {items.length ? renderTodoItemsList(items) : null}
      {event.text ? renderRichText(event.text, "messageBody", options) : null}
    </MessageSurface>
  );
}

function renderCustomMessageCard(event: MessageEvent, options: MarkdownRenderOptions) {
  const customType = typeof event.custom_type === "string" ? event.custom_type : "";
  const details = asRecord(event.details);

  if (isCodexSubagentMessage(event)) {
    const role = firstNonEmptyText(details?.role, event.summary, "message");
    const threadID = firstNonEmptyText(details?.thread_id);
    const title = role.toLowerCase() === "user" ? "Codex Subagent Prompt" : "Codex Subagent";
    return (
      <MessageSurface kind="custom_message" className="codexSubagentMessage">
        {renderCardHeader("custom_message", title, threadID ? `thread ${threadID}` : "subagent", event.ts)}
        <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
          <span className="block text-xs uppercase tracking-wide text-muted-foreground">Role</span>
          <strong>{role}</strong>
        </div>
        {event.text ? renderRichText(event.text, "messageBody", options) : null}
      </MessageSurface>
    );
  }

  if (customType === "claude-todo-v2-task-assignment") {
    return (
      <MessageSurface kind="custom_message">
        {renderCardHeader("custom_message", firstNonEmptyText(event.text, "Task assignment"), "Claude Todo V2", event.ts)}
        <div className="space-y-3">
          {event.subject ? (
            <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
              <span className="block text-xs uppercase tracking-wide text-muted-foreground">Subject</span>
              <strong>{event.subject}</strong>
            </div>
          ) : null}
          <div className="grid grid-cols-2 gap-2">
            {event.owner ? (
              <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
                <span className="block text-xs uppercase tracking-wide text-muted-foreground">Owner</span>
                <strong>{event.owner}</strong>
              </div>
            ) : null}
            {event.assigned_by ? (
              <div className="messageMetaItem rounded-xl bg-background/70 p-3 text-sm">
                <span className="block text-xs uppercase tracking-wide text-muted-foreground">Assigned By</span>
                <strong>{event.assigned_by}</strong>
              </div>
            ) : null}
          </div>
          {event.description ? renderRichText(event.description, "messageBody", options) : null}
        </div>
      </MessageSurface>
    );
  }

  const body = firstNonEmptyText(event.description, event.text, event.summary, event.name);
  const detailsText = event.details ? JSON.stringify(event.details, null, 2) : "";

  return (
    <MessageSurface kind="custom_message">
      {renderCardHeader("custom_message", firstNonEmptyText(event.text, event.summary, event.name, customType, "Custom message"), customType || undefined, event.ts)}
      {body ? renderRichText(body, "messageBody", options) : null}
      {detailsText ? <pre className="messageCardPre overflow-x-auto rounded-xl bg-background/80 p-3 text-sm">{detailsText}</pre> : null}
    </MessageSurface>
  );
}

function renderSystemCard(event: MessageEvent, kind: string, options: MarkdownRenderOptions) {
  const title = firstNonEmptyText(event.summary, event.name);
  const body = firstNonEmptyText(event.text, event.context, event.question, title === event.summary ? "" : event.summary);
  const detailsText = event.details ? JSON.stringify(event.details, null, 2) : "";
  const isToolResult = kind === "tool_result";
  const isRawCode = (kind === "tool" || kind === "tool_result") && isRawCodeToolOutput(event.name);
  const structured = isToolResult ? renderStructuredToolResult(event) : null;
  const todoItems = isToolResult ? todoItemsFromEvent(event) : [];
  const todoItemsFromBody = isToolResult ? todoItemsFromText(body) : [];
  const hideTodoJsonBody = todoItems.length > 0 && todoItemsFromBody.length > 0;

  return (
    <MessageSurface kind={kind} isError={event.is_error === true}>
      {renderCardHeader(kind, title || undefined, undefined, event.ts)}
      {structured}
      {todoItems.length ? renderTodoItemsList(todoItems) : null}
      {body && !hideTodoJsonBody ? (isRawCode ? renderCodeBlock(body) : renderRichText(body, "messageBody", options)) : null}
      {!todoItems.length && !structured && detailsText ? renderCodeBlock(detailsText) : null}
    </MessageSurface>
  );
}

function renderConversationEvent(
  event: MessageEvent,
  kind: string,
  sessionId: string | undefined,
  runtimeId: string | null | undefined,
  options: MarkdownRenderOptions,
  allowFuzzyLiveMatch = true,
  allowLegacyFallback = false,
  turnMeta?: AssistantTurnMeta,
  commandOutput?: boolean,
) {
  switch (kind) {
    case "system":
    case "user":
    case "assistant":
      return renderChatCard(event, kind, options, kind === "assistant" ? turnMeta : undefined, kind === "assistant" ? commandOutput : undefined);
    case "ask_user":
      return renderAskUserCard(event, sessionId, runtimeId, options, allowFuzzyLiveMatch, allowLegacyFallback);
    case "wait":
      return <WaitCard event={event} sessionId={sessionId} />;
    case "reasoning":
      return renderReasoningCard(event, options);
    case "tool":
    case "tool_result":
      return renderSystemCard(event, kind, options);
    case "team":
      return renderTeamCard(event, options);
    case "todo_snapshot":
      return renderTodoSnapshotCard(event, options);
    case "custom_message":
      return renderCustomMessageCard(event, options);
    case "pi_session":
    case "pi_model_change":
    case "pi_thinking_level_change":
    case "pi_event":
    case "event":
    case "error":
      return renderSystemCard(event, kind, options);
    default:
      return renderSystemCard(event, kind, options);
  }
}

function renderLoadingCards() {
  return (
    <div className="messageList flex flex-col gap-3">
      {Array.from({ length: 3 }, (_value, index) => (
        <div key={index} className={cn("messageRow flex", index === 0 ? "assistant" : index === 1 ? "tool" : "assistant")}>
          <Card data-testid="message-surface" data-kind="loading" className="messageSurface max-w-4xl rounded-[1.35rem] border border-border/60 bg-card/90 shadow-sm">
            <CardContent className="space-y-3 p-4">
              <div className="flex items-center gap-2">
                <Skeleton className="h-5 w-20 rounded-full" />
                <Skeleton className="h-4 w-36" />
              </div>
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-5/6" />
              {index === 1 ? <Skeleton className="h-16 w-full rounded-xl" /> : null}
            </CardContent>
          </Card>
        </div>
      ))}
    </div>
  );
}

function WorkingIndicator({ label = "Working" }: { label?: string }) {
  return (
    <div className="messageRow assistant workingIndicator flex px-1 py-1">
      <div className="flex items-center gap-2 rounded-2xl border border-border/40 bg-muted/30 px-3 py-2 text-muted-foreground/70 shadow-sm">
        <div className="flex gap-1">
          <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-current [animation-delay:-0.3s]" />
          <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-current [animation-delay:-0.15s]" />
          <span className="h-1.5 w-1.5 animate-bounce rounded-full bg-current" />
        </div>
        <span className="text-xs font-medium uppercase tracking-wider">{label}</span>
      </div>
    </div>
  );
}

function stableHash(value: string): string {
  let hash = 2166136261;
  for (let i = 0; i < value.length; i += 1) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}

function eventStableIdentity(event: MessageEvent, kind: string, index: number): string {
  const row = event as Record<string, unknown>;
  for (const field of ["message_id", "event_id", "stream_id", "tool_call_id", "request_id", "task_id"] as const) {
    const value = row[field];
    if (typeof value === "string" && value.trim()) {
      return `${field}:${value.trim()}`;
    }
  }
  const ts = eventTimestampSeconds(event);
  const signature = JSON.stringify({
    kind,
    ts,
    name: firstNonEmptyText(event.name, event.toolName),
    role: event.role,
    type: event.type,
    text: typeof event.text === "string" ? event.text : "",
    summary: typeof event.summary === "string" ? event.summary : "",
    details: event.details ?? null,
  });
  return `signature:${stableHash(signature)}:${index}`;
}

function messageRowKey(event: MessageEvent, kind: string, index: number) {
  return `${kind}:${eventStableIdentity(event, kind, index)}`;
}

function scrollPaneToBottom(element: HTMLElement) {
  if (typeof element.scrollTo === "function") {
    element.scrollTo({ top: element.scrollHeight });
    return;
  }
  element.scrollTop = element.scrollHeight;
}

const PREVIOUS_USER_BUTTON_SCROLL_THRESHOLD = 320;
const PREVIOUS_USER_BUTTON_VIEWPORT_RATIO = 0.5;
const PREVIOUS_USER_TARGET_TOLERANCE = 24;
const PREVIOUS_USER_SCROLL_TOP_PADDING = 16;

function ArrowUpTurnIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M5 6 8 3l3 3" />
      <path d="M8 3v7" />
      <path d="M8 10h3.5A2.5 2.5 0 0 1 14 12.5" />
    </svg>
  );
}

function ArrowDownIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M8 3v10" />
      <path d="m5 10 3 3 3-3" />
    </svg>
  );
}

function findPreviousUserRow(pane: HTMLElement): HTMLElement | null {
  const visibilityThreshold = pane.clientHeight > 0
    ? pane.clientHeight * PREVIOUS_USER_BUTTON_VIEWPORT_RATIO
    : PREVIOUS_USER_BUTTON_SCROLL_THRESHOLD;
  if (pane.scrollTop < visibilityThreshold) {
    return null;
  }

  const threshold = pane.scrollTop - PREVIOUS_USER_TARGET_TOLERANCE;

  const rows = Array.from(pane.querySelectorAll<HTMLElement>(".messageRow.user"));
  let candidate: HTMLElement | null = null;
  for (const row of rows) {
    if (row.offsetTop <= threshold) {
      candidate = row;
      continue;
    }
    break;
  }

  return candidate;
}

function paneDistanceFromBottom(pane: HTMLElement): number {
  const visibleHeight = pane.clientHeight > 0 ? pane.clientHeight : 0;
  if (visibleHeight > 0 && pane.scrollHeight > 0) {
    return Math.max(0, pane.scrollHeight - (pane.scrollTop + visibleHeight));
  }

  const rows = Array.from(pane.querySelectorAll<HTMLElement>(".messageRow"));
  const lastRow = rows[rows.length - 1] ?? null;
  const fallbackContentBottom = lastRow ? lastRow.offsetTop + lastRow.offsetHeight : 0;
  const contentBottom = Math.max(pane.scrollHeight, fallbackContentBottom);
  return Math.max(0, contentBottom - (pane.scrollTop + visibleHeight));
}

function shouldShowScrollToBottom(pane: HTMLElement): boolean {
  const visibleHeight = pane.clientHeight > 0 ? pane.clientHeight : 0;
  const threshold = visibleHeight > 0 ? Math.max(160, Math.round(visibleHeight * 0.5)) : 180;
  return paneDistanceFromBottom(pane) > threshold;
}

function shouldAutoFollowBottom(pane: HTMLElement): boolean {
  const visibleHeight = pane.clientHeight > 0 ? pane.clientHeight : 0;
  const threshold = visibleHeight > 0 ? Math.max(40, Math.round(visibleHeight * 0.12)) : 48;
  return paneDistanceFromBottom(pane) <= threshold;
}

function scrollPaneToPosition(element: HTMLElement, top: number) {
  const nextTop = Math.max(0, top);
  if (typeof element.scrollTo === "function") {
    element.scrollTo({ top: nextTop, behavior: "smooth" });
    return;
  }
  element.scrollTop = nextTop;
}

interface ConversationPaneProps {
  onOpenFilePath?: (path: string, line?: number | null) => void;
}

export function ConversationPane({ onOpenFilePath }: ConversationPaneProps) {
  const activeSessionId = useSessionsStoreSelector((state) => state.activeSessionId);
  const activeSession = useSessionsStoreSelector(
    selectConversationActiveSession,
    conversationActiveSessionEqual,
  );
  const { isGenerating, hasLiveBusy, liveBusy, liveSessionError } = useLiveSessionStoreSelector((state) => activeSessionId ? ({
    isGenerating: state.generatingBySessionId?.[activeSessionId] === true,
    hasLiveBusy: Object.prototype.hasOwnProperty.call(state.busyBySessionId ?? {}, activeSessionId),
    liveBusy: state.busyBySessionId?.[activeSessionId] === true,
    liveSessionError: String(state.errorBySessionId?.[activeSessionId] || "").trim(),
  }) : {
    isGenerating: false,
    hasLiveBusy: false,
    liveBusy: false,
    liveSessionError: "",
  }, shallowEqual);
  const composerStoreApi = useComposerStoreApi();
  const pendingMessages = useComposerStoreSelector((state) => activeSessionId ? state.pendingBySessionId?.[activeSessionId] ?? EMPTY_PENDING_MESSAGES : EMPTY_PENDING_MESSAGES);
  const {
    persistedMessages,
    hasOlder,
    olderCursor,
    olderLoading,
    activeSessionLoading,
    activeSessionLoaded,
  } = useMessagesStoreSelector((state) => activeSessionId ? ({
    persistedMessages: state.bySessionId[activeSessionId] ?? EMPTY_MESSAGES,
    hasOlder: state.hasOlderBySessionId?.[activeSessionId] === true,
    olderCursor: state.olderBeforeBySessionId?.[activeSessionId] ?? 0,
    olderLoading: state.loadingOlderBySessionId?.[activeSessionId] === true,
    activeSessionLoading: state.loadingBySessionId?.[activeSessionId] === true,
    activeSessionLoaded: state.loadedBySessionId?.[activeSessionId] === true,
  }) : {
    persistedMessages: EMPTY_MESSAGES,
    hasOlder: false,
    olderCursor: 0,
    olderLoading: false,
    activeSessionLoading: false,
    activeSessionLoaded: false,
  }, shallowEqual);
  const messagesStoreApi = useMessagesStoreApi();
  const liveSessionStoreApi = useLiveSessionStoreApi();
  const activeSessionRuntimeId = getSessionRuntimeId(activeSession);
  const [selectedView, setSelectedView] = useState<"conversation" | "trace">("conversation");
  useEffect(() => {
    setSelectedView("conversation");
  }, [activeSessionId]);
  const activeSessionIsPi = activeSession?.agent_backend === "pi";
  const activeSessionIsHistoricalPi = activeSession?.historical === true && activeSessionIsPi;
  const allowLegacyAskUserFallback = Boolean(activeSessionIsPi && activeSession?.transport !== "pi-rpc");
  const isBusy = Boolean(
    isGenerating
    || (hasLiveBusy ? liveBusy : activeSession?.busy === true),
  );
  const hasLocalConversationState = persistedMessages.length > 0 || pendingMessages.length > 0;
  const messages = useMemo(
    () => sortEventsByTimestamp(filterLocalUserEchoes(filterResolvedBridgePseudoEvents([...persistedMessages, ...pendingMessages], activeSessionIsPi)).filter(shouldRenderInMainConversation)),
    [activeSessionIsPi, pendingMessages, persistedMessages],
  );
  const assistantTurnMetaByIndex = useMemo(() => buildAssistantTurnMeta(messages), [messages]);
  const commandOutputByIndex = useMemo(() => commandOutputByAssistantIndex(messages), [messages]);
  const lastMessage = messages[messages.length - 1] ?? null;
  const latestMessageScrollKey = useMemo(() => (lastMessage
    ? [
      messages.length,
      eventKind(lastMessage),
      typeof lastMessage.event_id === "string" ? lastMessage.event_id : "",
      typeof lastMessage.stream_id === "string" ? lastMessage.stream_id : "",
      typeof lastMessage.text === "string" ? lastMessage.text.length : 0,
      lastMessage.streaming === true ? "streaming" : "durable",
    ].join(":")
    : "empty"), [lastMessage, messages.length]);
  const rows = useMemo(() => messages.reduce<Array<{
    key: string;
    kind: string;
    grouped: boolean;
    events: MessageEvent[];
    firstTs: number | null;
    lastTs: number | null;
    allowFuzzyLiveMatch: boolean;
    allowLegacyFallback: boolean;
    messageIndex: number;
    turnMeta?: AssistantTurnMeta;
    commandOutput?: boolean;
  }>>((out, message, index) => {
    const kind = eventKind(message);
    const traceKind = compactTraceKind(message);
    if (traceKind) {
      const last = out[out.length - 1];
      const ts = eventTimestampSeconds(message);
      if (last && last.kind === "machine_trace") {
        last.events.push(message);
        last.lastTs = ts;
        return out;
      }
      const rowKey = messageRowKey(message, kind, index);
      out.push({
        key: `machine:${rowKey}`,
        kind: "machine_trace",
        grouped: false,
        events: [message],
        firstTs: ts,
        lastTs: ts,
        allowFuzzyLiveMatch: true,
        allowLegacyFallback: false,
        messageIndex: index,
      });
      return out;
    }

    const rowKey = messageRowKey(message, kind, index);
    const ts = eventTimestampSeconds(message);
    const prevKind = index > 0 ? eventKind(messages[index - 1]) : null;
    out.push({
      key: rowKey,
      kind,
      grouped: prevKind === kind && canGroupEvent(kind),
      events: [message],
      firstTs: ts,
      lastTs: ts,
      allowFuzzyLiveMatch: kind === "ask_user" ? shouldAllowFuzzyAskUserMatch(messages, index) : true,
      allowLegacyFallback: kind === "ask_user" ? allowLegacyAskUserFallback : false,
      messageIndex: index,
      turnMeta: kind === "assistant" ? assistantTurnMetaByIndex.get(index) : undefined,
      commandOutput: kind === "assistant" ? commandOutputByIndex.has(index) : undefined,
    });
    return out;
  }, []), [allowLegacyAskUserFallback, assistantTurnMetaByIndex, commandOutputByIndex, messages]);
  const sectionRef = useRef<HTMLElement | null>(null);
  const historyAnchorRef = useRef<{ key: string; top: number } | null>(null);
  const scrollModeRef = useRef<"bottom" | "preserve" | null>(null);
  const autoFollowBottomRef = useRef(true);
  const [showPreviousUserJump, setShowPreviousUserJump] = useState(false);
  const [showScrollToBottom, setShowScrollToBottom] = useState(false);
  const [attemptedLoadOlder, setAttemptedLoadOlder] = useState(false);
  const [sessionSwitchLoadingId, setSessionSwitchLoadingId] = useState<string | null>(activeSessionId);
  const showHistoryControls = Boolean(activeSessionId && messages.length && (hasOlder || olderCursor > 0 || olderLoading));
  const showHistoryTopReached = Boolean(activeSessionId && messages.length && attemptedLoadOlder && !olderLoading && !hasOlder && olderCursor <= 0);
  const waitingForInitialHistoricalReplay = activeSessionIsHistoricalPi && messages.length === 0 && !activeSessionLoaded;
  const showLoadingState = Boolean(
    activeSessionId
    && !liveSessionError
    && messages.length === 0
    && !activeSessionLoaded
    && (
      activeSessionLoading
      || waitingForInitialHistoricalReplay
      || sessionSwitchLoadingId === activeSessionId
    )
  );
  const markdownOptions: MarkdownRenderOptions = useMemo(() => ({
    sessionId: activeSessionId || undefined,
    cwd: activeSession?.cwd,
    onOpenLocalFile: onOpenFilePath,
  }), [activeSessionId, activeSession?.cwd, onOpenFilePath]);

  useEffect(() => {
    setAttemptedLoadOlder(false);
    autoFollowBottomRef.current = true;
    if (!activeSessionId) {
      setSessionSwitchLoadingId(null);
      return;
    }
    if (!activeSessionLoaded && !hasLocalConversationState) {
      setSessionSwitchLoadingId(activeSessionId);
      return;
    }
    setSessionSwitchLoadingId((current) => (current === activeSessionId ? null : current));
  }, [activeSessionId, activeSessionLoaded, hasLocalConversationState]);

  useEffect(() => {
    if (!activeSessionId) return;
    if (pendingMessages.length === 0) return;
    const clearAcknowledgedPending = (composerStoreApi as { clearAcknowledgedPending?: (sessionId: string, events: MessageEvent[]) => void }).clearAcknowledgedPending;
    if (typeof clearAcknowledgedPending !== "function") return;
    clearAcknowledgedPending(activeSessionId, activeSessionIsPi ? persistedMessages.filter((event) => event.role !== "user" || isPiConfirmedUserEvent(event) || event.request_state === "failed") : persistedMessages);
  }, [activeSessionId, activeSessionIsPi, persistedMessages, pendingMessages.length, composerStoreApi]);

  const recomputeFloatingNavigation = () => {
    const pane = sectionRef.current?.querySelector(".conversationPane") as HTMLElement | null;
    if (!pane || !activeSessionId) {
      setShowPreviousUserJump(false);
      setShowScrollToBottom(false);
      return;
    }
    setShowPreviousUserJump(Boolean(findPreviousUserRow(pane)));
    setShowScrollToBottom(shouldShowScrollToBottom(pane));
  };

  useLayoutEffect(() => {
    const pane = sectionRef.current?.querySelector(".conversationPane") as HTMLElement | null;
    if (!pane || (!messages.length && !isBusy)) {
      setShowPreviousUserJump(false);
      setShowScrollToBottom(false);
      return;
    }

    if (scrollModeRef.current === "preserve") {
      const anchor = historyAnchorRef.current;
      if (anchor) {
        const anchorRow = pane.querySelector(`[data-row-key="${anchor.key}"]`) as HTMLElement | null;
        if (anchorRow) {
          pane.scrollTop = Math.max(0, anchorRow.offsetTop - anchor.top);
        }
      }
      historyAnchorRef.current = null;
      scrollModeRef.current = null;
      autoFollowBottomRef.current = shouldAutoFollowBottom(pane);
      recomputeFloatingNavigation();
      return;
    }

    if (scrollModeRef.current === "bottom" || autoFollowBottomRef.current) {
      scrollPaneToBottom(pane);
      autoFollowBottomRef.current = true;
      scrollModeRef.current = null;
      recomputeFloatingNavigation();
      return;
    }

    scrollModeRef.current = null;
    recomputeFloatingNavigation();
  }, [messages.length, latestMessageScrollKey, activeSessionId, isBusy]);

  useEffect(() => {
    const pane = sectionRef.current?.querySelector(".conversationPane") as HTMLElement | null;
    if (!pane || !activeSessionId) {
      return undefined;
    }

    const onScroll = () => {
      autoFollowBottomRef.current = shouldAutoFollowBottom(pane);
      if (pane.scrollTop <= 12 && !olderLoading && (hasOlder || olderCursor > 0)) {
        void handleLoadOlder();
      }
      setShowPreviousUserJump(Boolean(findPreviousUserRow(pane)));
      setShowScrollToBottom(shouldShowScrollToBottom(pane));
    };

    pane.addEventListener("scroll", onScroll, { passive: true });
    return () => pane.removeEventListener("scroll", onScroll);
  }, [activeSessionId, hasOlder, olderCursor, olderLoading]);

  const handleLoadOlder = async () => {
    if (!activeSessionId) return;
    const pane = sectionRef.current?.querySelector(".conversationPane") as HTMLElement | null;
    const firstRow = pane?.querySelector("[data-row-key]") as HTMLElement | null;
    if (pane && firstRow) {
      historyAnchorRef.current = {
        key: String(firstRow.dataset.rowKey || ""),
        top: firstRow.offsetTop - pane.scrollTop,
      };
      scrollModeRef.current = "preserve";
    }
    autoFollowBottomRef.current = false;
    setAttemptedLoadOlder(true);
    await messagesStoreApi.loadOlder(activeSessionId);
  };

  useEffect(() => {
    if (!activeSessionId || !activeSessionIsHistoricalPi) {
      return;
    }
    if (activeSessionLoaded || activeSessionLoading) {
      return;
    }
    void messagesStoreApi.loadInitial(activeSessionId);
  }, [activeSessionId, activeSessionIsHistoricalPi, activeSessionLoaded, activeSessionLoading, messagesStoreApi]);

  const handleJumpToLatest = async () => {
    if (!activeSessionId) return;
    historyAnchorRef.current = null;
    scrollModeRef.current = "bottom";
    autoFollowBottomRef.current = true;
    if (activeSessionIsHistoricalPi) {
      await messagesStoreApi.loadInitial(activeSessionId);
      return;
    }
    await liveSessionStoreApi.loadInitial(activeSessionId);
  };

  const handleJumpToPreviousUser = () => {
    const pane = sectionRef.current?.querySelector(".conversationPane") as HTMLElement | null;
    if (!pane) return;
    const target = findPreviousUserRow(pane);
    if (!target) {
      setShowPreviousUserJump(false);
      return;
    }
    scrollPaneToPosition(pane, target.offsetTop - PREVIOUS_USER_SCROLL_TOP_PADDING);
  };

  const handleScrollToBottom = () => {
    const pane = sectionRef.current?.querySelector(".conversationPane") as HTMLElement | null;
    if (!pane) return;
    autoFollowBottomRef.current = true;
    scrollPaneToPosition(pane, pane.scrollHeight);
  };

  return (
    <section ref={sectionRef} className="conversationTimeline relative flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b border-border/50 px-3 py-2 text-sm">
        <button
          type="button"
          className={cn("rounded-full px-3 py-1.5 transition", selectedView === "conversation" ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-accent hover:text-accent-foreground")}
          onClick={() => setSelectedView("conversation")}
          aria-pressed={selectedView === "conversation"}
        >
          Conversation
        </button>
        <button
          type="button"
          className={cn("rounded-full px-3 py-1.5 transition", selectedView === "trace" ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-accent hover:text-accent-foreground")}
          onClick={() => setSelectedView("trace")}
          aria-pressed={selectedView === "trace"}
        >
          Trace
        </button>
      </div>
      <ScrollArea
        key={`${activeSessionId || "no-session"}:${selectedView}`}
        className={cn("conversationPane conversationScrollArea min-h-0 flex-1 px-3 py-4", !activeSessionId && "emptyState")}
      >
        {showLoadingState ? (
          renderLoadingCards()
        ) : selectedView === "trace" ? (
          <TraceView sessionId={activeSessionId} runtimeId={activeSessionRuntimeId} messages={messages} />
        ) : (
          <div className="messageList flex flex-col gap-3">
            {showHistoryControls || showHistoryTopReached ? (
              <div className="historyControls flex flex-wrap items-center justify-between gap-2 rounded-[1.1rem] border border-border/60 bg-background/70 px-3 py-2 text-sm text-muted-foreground">
                <span>
                  {showHistoryTopReached
                    ? "已经到顶了"
                    : hasOlder || olderCursor > 0
                      ? "Older conversation history is available."
                      : "You are viewing older history."}
                </span>
                <div className="flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    className="inline-flex items-center rounded-full border border-border/70 px-3 py-1.5 font-medium text-foreground transition hover:bg-accent hover:text-accent-foreground disabled:cursor-not-allowed disabled:opacity-60"
                    onClick={() => void handleLoadOlder()}
                    disabled={olderLoading || !activeSessionId || (!hasOlder && olderCursor <= 0)}
                  >
                    {olderLoading ? "Loading..." : "Load older"}
                  </button>
                  <button
                    type="button"
                    className="inline-flex items-center rounded-full border border-border/70 px-3 py-1.5 font-medium text-foreground transition hover:bg-accent hover:text-accent-foreground disabled:cursor-not-allowed disabled:opacity-60"
                    onClick={() => void handleJumpToLatest()}
                    disabled={!activeSessionId}
                  >
                    Jump to latest
                  </button>
                </div>
              </div>
            ) : null}
            {liveSessionError ? (
              <MessageSurface kind="event" isError>
                {renderCardHeader("event", "Pi RPC warning")}
                <div className="messageCardFooterText text-sm text-foreground">{liveSessionError}</div>
              </MessageSurface>
            ) : null}
            {rows.length ? (
              rows.map((row, index) => {
                const ts = row.firstTs;
                const prevTs = index > 0 ? rows[index - 1]?.lastTs ?? null : null;
                const showDaySeparator = isDisplayableEpochTs(ts) && (!isDisplayableEpochTs(prevTs) || messageDayKey(prevTs) !== messageDayKey(ts));
                return (
                  <Fragment key={row.key}>
                    {showDaySeparator ? (
                      <div className="daySeparator flex items-center gap-3 py-1 text-xs uppercase tracking-[0.16em] text-muted-foreground">
                        <span className="h-px flex-1 bg-border/60" />
                        <span>{formatDaySeparator(ts)}</span>
                        <span className="h-px flex-1 bg-border/60" />
                      </div>
                    ) : null}
                    <div data-row-key={row.key} className={cn("messageRow flex", row.kind, row.grouped && "grouped")}>
                      {row.kind === "machine_trace"
                        ? <CompactMachineTrace events={row.events} options={markdownOptions} isBusy={isBusy && index === rows.length - 1} />
                        : renderConversationEvent(
                          row.events[0],
                          row.kind,
                          activeSessionId || undefined,
                          activeSessionRuntimeId,
                          markdownOptions,
                          row.allowFuzzyLiveMatch,
                          row.allowLegacyFallback,
                          row.turnMeta,
                          row.commandOutput,
                        )}
                    </div>
                  </Fragment>
                );
              })
            ) : (
              <Card className="rounded-[1.35rem] border-dashed border-border/60 bg-muted/20 shadow-none">
                <CardContent className="p-6 text-sm text-muted-foreground">
                  {activeSessionId ? "No conversation events yet." : "Select a session to see its conversation timeline."}
                </CardContent>
              </Card>
            )}
            {isBusy && <WorkingIndicator label={isGenerating ? "Generating" : "Working"} />}
          </div>
        )}
      </ScrollArea>
      {selectedView === "conversation" && (showPreviousUserJump || showScrollToBottom) ? (
        <div className="conversationNavButtons">
          {showPreviousUserJump ? (
            <Button
              data-testid="jump-to-previous-user"
              type="button"
              variant="secondary"
              size="icon"
              className="conversationJumpButton shadow-lg"
              onClick={handleJumpToPreviousUser}
              aria-label="Jump to previous user message"
            >
              <ArrowUpTurnIcon />
            </Button>
          ) : null}
          {showScrollToBottom ? (
            <Button
              data-testid="scroll-to-bottom"
              type="button"
              variant="secondary"
              size="icon"
              className="conversationJumpButton shadow-lg"
              onClick={handleScrollToBottom}
              aria-label="Scroll to conversation bottom"
            >
              <ArrowDownIcon />
            </Button>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
