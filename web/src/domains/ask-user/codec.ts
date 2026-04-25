import type { SessionUiRequest } from "../../lib/types";
import { ASK_USER_BRIDGE_PREFIX, type AskUserBridgeAnswers, type AskUserBridgeRequest, type AskUserBridgeQuestion } from "./contract";
import { normalizeAskUserBridgeQuestions } from "./normalize";

export function parseAskUserBridgeRequest(request: SessionUiRequest | undefined | null): AskUserBridgeRequest | null {
  if (!request || request.method !== "editor") return null;
  const prefill = typeof request.prefill === "string" ? request.prefill : "";
  if (!prefill.startsWith(`${ASK_USER_BRIDGE_PREFIX}\n`)) return null;

  try {
    const parsed = JSON.parse(prefill.slice(ASK_USER_BRIDGE_PREFIX.length + 1)) as {
      questions?: unknown;
      metadata?: Record<string, unknown>;
    };
    const questions = normalizeAskUserBridgeQuestions(parsed.questions);
    return questions.length ? { questions, metadata: parsed.metadata } : null;
  } catch {
    return null;
  }
}

export function encodeAskUserBridgeResponse(answers: AskUserBridgeAnswers) {
  return `${ASK_USER_BRIDGE_PREFIX}\n${JSON.stringify({ action: "answered", answers })}`;
}

export function askUserBridgeQuestionSignature(questions: AskUserBridgeQuestion[]) {
  return questions
    .map((question) => [
      question.header,
      question.question,
      question.multiSelect ? "multi" : "single",
      question.options.map((option) => option.label).join("\u0001"),
    ].join("\u0002"))
    .join("\u0003");
}
