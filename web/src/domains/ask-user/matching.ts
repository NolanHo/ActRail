import type { MessageEvent, SessionUiRequest } from "../../lib/types";
import { askUserBridgeQuestionSignature, parseAskUserBridgeRequest } from "./codec";
import { askUserContextText, askUserEventQuestions, askUserOptionSignature, askUserPromptText, askUserRequestId } from "./normalize";

export function askUserHistorySignature(event: MessageEvent) {
  const questions = askUserEventQuestions(event);
  if (questions.length) {
    return `bridge\u0002${askUserBridgeQuestionSignature(questions)}`;
  }
  const prompt = askUserPromptText(event);
  if (!prompt) return "";
  return [prompt, askUserContextText(event), askUserOptionSignature(Array.isArray(event.options) ? event.options : undefined)].join("\u0002");
}

export function isUnresolvedAskUserEvent(event: MessageEvent) {
  return event.type === "ask_user" && !event.resolved && !event.answer && !event.cancelled;
}

export function findMatchingLiveRequest(event: MessageEvent, requests: SessionUiRequest[], allowFuzzyLiveMatch: boolean) {
  const directRequestId = askUserRequestId(event);
  const direct = requests.find((request) => askUserRequestId(request) === directRequestId);
  if (direct) return direct;

  if (!allowFuzzyLiveMatch) return undefined;

  const prompt = askUserPromptText(event);
  const eventQuestions = askUserEventQuestions(event);
  if (!prompt && !eventQuestions.length) return undefined;
  const context = askUserContextText(event);
  const optionSignature = askUserOptionSignature(Array.isArray(event.options) ? event.options : undefined);
  const matches = requests.filter((request) => {
    const bridgeRequest = parseAskUserBridgeRequest(request);
    if (bridgeRequest && eventQuestions.length) {
      return askUserBridgeQuestionSignature(bridgeRequest.questions) === askUserBridgeQuestionSignature(eventQuestions);
    }
    if (askUserPromptText(request) !== prompt) return false;
    if (askUserContextText(request) !== context) return false;
    return askUserOptionSignature(Array.isArray(request.options) ? request.options : undefined) === optionSignature;
  });
  if (matches.length === 1) return matches[0];
  return undefined;
}
