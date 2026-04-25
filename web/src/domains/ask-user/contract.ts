import type { MessageEvent, SessionUiRequest } from "../../lib/types";

export type OptionInput = { label?: string; value?: string; title?: string; description?: string } | string;

export type AskUserBridgeOption = {
  label: string;
  description?: string;
  preview?: string;
};

export type AskUserBridgeQuestion = {
  header: string;
  question: string;
  options: AskUserBridgeOption[];
  multiSelect?: boolean;
};

export type AskUserBridgeRequest = {
  questions: AskUserBridgeQuestion[];
  metadata?: Record<string, unknown>;
};

export type AskUserBridgeAnswers = Record<string, string | string[]>;

export type AskUserLike = MessageEvent | SessionUiRequest;

export const ASK_USER_BRIDGE_PREFIX = "__codoxear_ask_user_bridge_v1__";
export const CUSTOM_RESPONSE_OPTION_RE = /type custom response/i;
