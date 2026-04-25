import type { MessageEvent, SessionUiRequest } from "../../lib/types";
import { CUSTOM_RESPONSE_OPTION_RE, type AskUserBridgeQuestion, type AskUserLike, type OptionInput } from "./contract";

export function splitAskUserTitle(value: string) {
  const text = value.trim();
  if (!text) return { prompt: "", context: "" };
  const marker = "\n\nContext:\n";
  const index = text.indexOf(marker);
  if (index < 0) return { prompt: text, context: "" };
  return {
    prompt: text.slice(0, index).trim(),
    context: text.slice(index + marker.length).trim(),
  };
}

export function normalizeOption(option: OptionInput, index: number) {
  if (typeof option === "string") {
    return { title: option, label: option, description: "", value: option, key: option || String(index) };
  }

  const title = option.title ?? option.label ?? option.value ?? `Option ${index + 1}`;
  const label = option.label ?? option.title ?? option.value ?? title;
  const value = String(option.value ?? option.title ?? title ?? "");

  return {
    title,
    label,
    description: option.description ?? "",
    value,
    key: value || String(index),
  };
}

export function askUserRequestId(ev: AskUserLike) {
  if (ev && typeof ev.requestId === "string" && ev.requestId) return ev.requestId;
  if (ev && typeof ev.id === "string" && ev.id) return ev.id;
  if (ev && typeof ev.tool_call_id === "string" && ev.tool_call_id) return ev.tool_call_id;
  return "";
}

export function askUserPromptText(ev: AskUserLike) {
  if (ev && typeof ev.question === "string" && ev.question.trim()) return ev.question.trim();
  if (ev && typeof ev.message === "string" && ev.message.trim()) return ev.message.trim();
  if (ev && typeof ev.title === "string" && ev.title.trim()) return splitAskUserTitle(ev.title).prompt;
  return "";
}

export function askUserContextText(ev: AskUserLike) {
  if (ev && typeof ev.context === "string" && ev.context.trim()) return ev.context.trim();
  if (ev && typeof ev.title === "string" && ev.title.trim()) return splitAskUserTitle(ev.title).context;
  return "";
}

export function buildPromptFallbackMessage(question: string, values: string[], freeform: string) {
  const trimmedFreeform = freeform.trim();
  const finalValues = [...values];
  if (trimmedFreeform) finalValues.push(trimmedFreeform);
  const answer = finalValues.join(", ").trim();
  if (!answer) return "";
  const escapedQuestion = question.replace(/"/g, '\\"');
  const escapedAnswer = answer.replace(/"/g, '\\"');
  return question ? `"${escapedQuestion}"="${escapedAnswer}"` : escapedAnswer;
}

export function normalizeAskUserBridgeQuestions(value: unknown): AskUserBridgeQuestion[] {
  if (!Array.isArray(value)) return [];

  const questions: AskUserBridgeQuestion[] = [];
  for (const item of value) {
    if (!item || typeof item !== "object") continue;
    const row = item as Record<string, unknown>;
    const header = typeof row.header === "string" ? row.header.trim() : "";
    const question = typeof row.question === "string" ? row.question.trim() : "";
    const options: AskUserBridgeQuestion["options"] = [];
    if (Array.isArray(row.options)) {
      for (const option of row.options) {
        if (!option || typeof option !== "object") continue;
        const value = option as Record<string, unknown>;
        const label = typeof value.label === "string" ? value.label.trim() : "";
        if (!label) continue;
        options.push({
          label,
          description: typeof value.description === "string" ? value.description.trim() : undefined,
          preview: typeof value.preview === "string" ? value.preview : undefined,
        });
      }
    }
    if (!header || !question || !options.length) continue;
    questions.push({
      header,
      question,
      options,
      multiSelect: row.multiSelect === true,
    });
  }
  return questions;
}

export function askUserEventQuestions(event: MessageEvent) {
  return normalizeAskUserBridgeQuestions(event.questions);
}

export function askUserOptionSignature(options: Array<OptionInput> | undefined) {
  if (!Array.isArray(options) || !options.length) return "";
  return options
    .map((option, index) => normalizeOption(option, index).title)
    .filter((signature) => !CUSTOM_RESPONSE_OPTION_RE.test(signature))
    .join("\u0001");
}

export function getInitialDraftValue(request: SessionUiRequest) {
  if (Array.isArray(request.value)) {
    return request.value.filter((item): item is string => typeof item === "string");
  }
  if (typeof request.value === "string") {
    return request.value;
  }
  if (request.method === "select" && Array.isArray(request.options) && request.options.length > 0 && !request.allow_multiple) {
    return normalizeOption(request.options[0], 0).value;
  }
  return request.allow_multiple ? [] : "";
}

export function normalizeRequestValue(request: SessionUiRequest, draftValue: string | string[]) {
  if (request.method === "confirm") {
    return undefined;
  }
  if (request.allow_multiple) {
    return Array.isArray(draftValue) ? draftValue : draftValue ? [draftValue] : [];
  }
  return Array.isArray(draftValue) ? draftValue[0] ?? "" : draftValue;
}
