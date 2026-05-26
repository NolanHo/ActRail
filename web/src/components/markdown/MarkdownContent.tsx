import { Fragment, type ComponentChildren, h } from "preact";
import { useMemo } from "preact/hooks";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import { unified } from "unified";

import { cn } from "../../lib/utils";

export interface MarkdownRenderOptions {
  sessionId?: string;
  cwd?: string;
  onOpenLocalFile?: (path: string, line?: number | null) => void;
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
const codeBlockUrlPattern = /\bhttps?:\/\/[^\s<>"'`]+/gi;

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

export function rewriteOaiMemCitations(rawText: string): string {
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

function splitCodeBlockUrl(rawUrl: string): { href: string; suffix: string } {
  const match = rawUrl.match(/[.,;:!?]+$/);
  if (!match) {
    return { href: rawUrl, suffix: "" };
  }
  const suffix = match[0] || "";
  return { href: rawUrl.slice(0, -suffix.length), suffix };
}

function renderCodeBlockText(rawValue: string, keyPrefix: string): ComponentChildren {
  const value = rawValue.replace(/\n$/, "");
  const parts: ComponentChildren[] = [];
  let cursor = 0;
  for (const match of value.matchAll(codeBlockUrlPattern)) {
    const matchedText = match[0] || "";
    const index = match.index ?? 0;
    if (!matchedText || index < cursor) {
      continue;
    }
    if (index > cursor) {
      parts.push(value.slice(cursor, index));
    }
    const { href, suffix } = splitCodeBlockUrl(matchedText);
    if (href) {
      parts.push(
        <a
          className="messageCodeLink underline decoration-dotted underline-offset-4"
          href={href}
          key={`${keyPrefix}-url-${index}`}
          rel="noreferrer"
          target="_blank"
        >
          {href}
        </a>,
      );
    }
    if (suffix) {
      parts.push(suffix);
    }
    cursor = index + matchedText.length;
  }
  if (!parts.length) {
    return value;
  }
  if (cursor < value.length) {
    parts.push(value.slice(cursor));
  }
  return parts;
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
          <code className={cn("font-mono text-sm", className)}>{renderCodeBlockText(node.value || "", keyPrefix)}</code>
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

export function MarkdownContent({ value, options = {} }: { value: string; options?: MarkdownRenderOptions }) {
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
