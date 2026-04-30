import type { MessageEvent } from "./types";

export type ContextTokenBucket = "systemPrompt" | "tool" | "user" | "assist";

export interface ContextTokenUsageBucket {
  tokens: number;
  chars: number;
  percent: number;
}

export interface ContextTokenUsageResult {
  buckets: Record<ContextTokenBucket, ContextTokenUsageBucket>;
  totalTokens: number;
  model: string;
  fallback: boolean;
  fallbackReason: string;
}

const EMPTY_BUCKETS: Record<ContextTokenBucket, ContextTokenUsageBucket> = {
  systemPrompt: { tokens: 0, chars: 0, percent: 0 },
  tool: { tokens: 0, chars: 0, percent: 0 },
  user: { tokens: 0, chars: 0, percent: 0 },
  assist: { tokens: 0, chars: 0, percent: 0 },
};

function normalizeString(value: unknown) {
  return typeof value === "string" && value.length > 0 ? value : "";
}

function contentText(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((item) => {
    if (!item || typeof item !== "object") {
      return [] as string[];
    }
    const text = normalizeString((item as { text?: unknown }).text);
    return text ? [text] : [];
  });
}

function safeJSONString(value: unknown) {
  if (!value || typeof value !== "object") {
    return "";
  }
  try {
    return JSON.stringify(value);
  } catch {
    return "";
  }
}

function eventText(event: MessageEvent, bucket: ContextTokenBucket) {
  const parts = [
    normalizeString(event.text),
    normalizeString(event.summary),
    normalizeString(event.output),
    normalizeString(event.question),
    normalizeString(event.context),
    ...contentText(event.message?.content),
  ];
  if (bucket === "tool") {
    parts.push(
      normalizeString(event.name),
      normalizeString(event.toolName),
      normalizeString(event.operation),
      safeJSONString(event.details),
    );
  }
  return parts.filter(Boolean).join("\n");
}

function bucketForEvent(event: MessageEvent): ContextTokenBucket | null {
  const role = normalizeString(event.role).toLowerCase();
  const type = normalizeString(event.type || event.kind).toLowerCase();
  if (type === "tool" || type === "tool_result" || role === "tool" || role === "toolresult" || event.tool_call_id || event.toolName) {
    return "tool";
  }
  if (role === "system" || type === "system" || type === "system_prompt") {
    return "systemPrompt";
  }
  if (role === "user") {
    return "user";
  }
  if (role === "assistant" || type === "reasoning") {
    return "assist";
  }
  return null;
}

function charsToTokens(chars: number) {
  return Math.ceil(Math.max(0, chars) / 4);
}

export async function calculateContextTokenUsage(messages: MessageEvent[], model: string | null | undefined): Promise<ContextTokenUsageResult> {
  const modelName = typeof model === "string" ? model.trim() : "";
  const textByBucket: Record<ContextTokenBucket, string[]> = {
    systemPrompt: [],
    tool: [],
    user: [],
    assist: [],
  };

  for (const event of messages) {
    const bucket = bucketForEvent(event);
    if (!bucket) {
      continue;
    }
    const text = eventText(event, bucket);
    if (text) {
      textByBucket[bucket].push(text);
    }
  }

  const fallback = true;
  const fallbackReason = "using chars/4 estimate";

  const buckets = { ...EMPTY_BUCKETS };
  for (const key of Object.keys(textByBucket) as ContextTokenBucket[]) {
    const text = textByBucket[key].join("\n");
    const chars = text.length;
    const tokens = charsToTokens(chars);
    buckets[key] = { tokens, chars, percent: 0 };
  }
  const totalTokens = Object.values(buckets).reduce((sum, bucket) => sum + bucket.tokens, 0);
  for (const key of Object.keys(buckets) as ContextTokenBucket[]) {
    buckets[key] = {
      ...buckets[key],
      percent: totalTokens > 0 ? Math.round((buckets[key].tokens / totalTokens) * 1000) / 10 : 0,
    };
  }

  return {
    buckets,
    totalTokens,
    model: modelName,
    fallback,
    fallbackReason,
  };
}
