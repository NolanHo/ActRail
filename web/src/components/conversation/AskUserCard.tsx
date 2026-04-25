import { useEffect, useState } from "preact/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import type { MessageEvent, SessionUiRequest } from "../../lib/types";
import { api } from "../../lib/api";
import { useLiveSessionStore, useLiveSessionStoreApi, useSessionUiStore, useSessionUiStoreApi } from "../../app/providers";
import { encodeAskUserBridgeResponse, parseAskUserBridgeRequest } from "../../domains/ask-user/codec";
import { findMatchingLiveRequest } from "../../domains/ask-user/matching";
import {
  askUserContextText,
  askUserEventQuestions,
  askUserPromptText,
  askUserRequestId,
  buildPromptFallbackMessage,
  normalizeOption,
} from "../../domains/ask-user/normalize";

interface AskUserCardProps {
  event: MessageEvent;
  sessionId?: string;
  runtimeId?: string | null;
  renderRichText: (value: string, className?: string) => any;
  allowFuzzyLiveMatch?: boolean;
  allowLegacyFallback?: boolean;
}

export { askUserHistorySignature, isUnresolvedAskUserEvent } from "../../domains/ask-user/matching";

export function AskUserCard({
  event,
  sessionId,
  runtimeId,
  renderRichText,
  allowFuzzyLiveMatch = true,
  allowLegacyFallback = false,
}: AskUserCardProps) {
  const liveSessionState = useLiveSessionStore();
  const liveSessionStoreApi = useLiveSessionStoreApi();
  const sessionUiState = useSessionUiStore() as { requests?: SessionUiRequest[] };
  const sessionUiStoreApi = useSessionUiStoreApi();
  const liveRequests = sessionId ? liveSessionState.requestsBySessionId[sessionId] ?? [] : [];
  const requests = liveRequests.length ? liveRequests : Array.isArray(sessionUiState.requests) ? sessionUiState.requests : [];

  const liveRequest = findMatchingLiveRequest(event, requests, allowFuzzyLiveMatch);
  const liveBridgeRequest = parseAskUserBridgeRequest(liveRequest);
  const requestId = askUserRequestId(liveRequest ?? event);
  const usingLegacyFallback = Boolean(allowLegacyFallback && !liveRequest && requestId);

  const resolved = Boolean(event.resolved || (event.answer && !liveRequest));
  const allowMultiple = Boolean(liveRequest?.allow_multiple ?? event.allow_multiple);
  const allowFreeform = Boolean(liveRequest?.allow_freeform ?? event.allow_freeform ?? true);

  const options = (
    Array.isArray(liveRequest?.options) && liveRequest.options.length
      ? liveRequest.options
      : Array.isArray(event.options)
        ? event.options
        : []
  ).map(normalizeOption);

  const [selectedValues, setSelectedValues] = useState<string[]>([]);
  const [freeformValue, setFreeformValue] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [awaitingLogSync, setAwaitingLogSync] = useState(false);
  const [bridgeAnswers, setBridgeAnswers] = useState<Record<string, string | string[]>>({});
  const promptFallbackMode = Boolean(!liveRequest && requestId && sessionId && event.prompt_fallback_available);
  const promptFallbackAvailable = Boolean(promptFallbackMode && !submitting && !awaitingLogSync);
  const bridgeQuestions = liveBridgeRequest?.questions ?? askUserEventQuestions(event);
  const resolvedBridgeAnswers =
    event.answers_by_question && typeof event.answers_by_question === "object"
      ? event.answers_by_question as Record<string, string | string[]>
      : {};
  const bridgeReady = bridgeQuestions.length
    ? bridgeQuestions.every((question) => {
        const answer = bridgeAnswers[question.question];
        return Array.isArray(answer) ? answer.length > 0 : typeof answer === "string" && answer.trim().length > 0;
      })
    : false;

  const prompt = askUserPromptText(liveRequest ?? event) || event.text || "Prompt";
  const context = askUserContextText(liveRequest ?? event);
  const answerText = Array.isArray(event.answer) ? event.answer.join(", ") : event.answer;
  const cardResolved = resolved && !promptFallbackAvailable;

  const isConfirm = event.method === "confirm" || liveRequest?.method === "confirm";
  const canAnswer = Boolean(
    sessionId &&
      requestId &&
      !submitting &&
      !awaitingLogSync &&
      (!resolved || promptFallbackAvailable) &&
      (liveRequest || usingLegacyFallback || promptFallbackAvailable)
  );

  useEffect(() => {
    if ((resolved && !promptFallbackMode) || liveRequest) {
      setAwaitingLogSync(false);
    }
  }, [liveRequest, promptFallbackMode, resolved]);

  const refreshAfterSubmit = async () => {
    if (!sessionId) return;
    if (liveRequests.length) {
      await (runtimeId
        ? liveSessionStoreApi.loadInitial(sessionId, runtimeId)
        : liveSessionStoreApi.loadInitial(sessionId));
      return;
    }
    await (runtimeId
      ? sessionUiStoreApi.refresh(sessionId, { agentBackend: "pi", runtimeId })
      : sessionUiStoreApi.refresh(sessionId, { agentBackend: "pi" }));
  };

  const handleSubmit = async (values: string[], freeform: string) => {
    if (!sessionId || !requestId) return;

    setSubmitting(true);
    setError("");

    try {
      if (liveBridgeRequest) {
        await (runtimeId
          ? api.submitUiResponse(sessionId, {
              id: requestId,
              value: encodeAskUserBridgeResponse(bridgeAnswers),
            }, runtimeId)
          : api.submitUiResponse(sessionId, {
              id: requestId,
              value: encodeAskUserBridgeResponse(bridgeAnswers),
            }));
        await refreshAfterSubmit();
        return;
      }

      if (promptFallbackAvailable) {
        const message = buildPromptFallbackMessage(prompt, values, freeform);
        if (!message) {
          setError("Enter an answer before submitting");
          return;
        }
        await (runtimeId ? api.sendMessage(sessionId, message, runtimeId) : api.sendMessage(sessionId, message));
        setAwaitingLogSync(true);
        return;
      }

      let finalValue: string | string[] | undefined;
      if (allowMultiple) {
        finalValue = [...values];
        if (freeform.trim()) finalValue.push(freeform.trim());
      } else {
        finalValue = freeform.trim() || values[0];
      }

      const payload = isConfirm
        ? { id: requestId, confirmed: true }
        : { id: requestId, value: finalValue };

      await (runtimeId ? api.submitUiResponse(sessionId, payload, runtimeId) : api.submitUiResponse(sessionId, payload));
      if (usingLegacyFallback) {
        setAwaitingLogSync(true);
      }
      await refreshAfterSubmit();
    } catch (e) {
      setAwaitingLogSync(false);
      setError(e instanceof Error ? e.message : "Failed to submit answer");
    } finally {
      setSubmitting(false);
    }
  };

  const isSelected = (value: string) => selectedValues.includes(value);

  const toggleOption = (value: string) => {
    if (!allowMultiple) {
      void handleSubmit([value], "");
      return;
    }

    setSelectedValues((current) =>
      current.includes(value) ? current.filter((v) => v !== value) : [...current, value]
    );
  };

  return (
    <div data-testid="message-surface" data-kind="ask_user" className={cn("messageCard ask_user", cardResolved && "resolved")}>
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <Badge variant="outline" className="askUserBadge">Ask user</Badge>
          {event.ts && (
             <time className="text-xs text-muted-foreground">
               {new Date(event.ts * 1000).toLocaleTimeString()}
             </time>
          )}
        </div>

        {context ? renderRichText(context, "askUserContext text-sm text-muted-foreground") : null}

        <div className="askUserQuestion text-base font-semibold">
          {prompt}
        </div>

        {bridgeQuestions.length > 0 && (
          <div className="flex flex-col gap-3">
            {bridgeQuestions.map((question) => {
              const currentAnswer = bridgeAnswers[question.question] ?? resolvedBridgeAnswers[question.question];
              const selected = Array.isArray(currentAnswer)
                ? currentAnswer
                : typeof currentAnswer === "string"
                  ? [currentAnswer]
                  : [];
              return (
                <div key={question.question} className="rounded-2xl border border-border/60 bg-background/60 p-3">
                  <div className="mb-2">
                    <div className="text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground">{question.header}</div>
                    <div className="text-sm font-semibold text-foreground">{question.question}</div>
                  </div>
                  <div className="askUserOptions flex flex-wrap gap-2">
                    {question.options.map((option) => {
                      const isSelected = selected.includes(option.label);
                      return (
                        <button
                          key={`${question.question}-${option.label}`}
                          type="button"
                          disabled={!canAnswer}
                          onClick={() => {
                            setBridgeAnswers((current) => {
                              const existing = current[question.question];
                              const previous = Array.isArray(existing)
                                ? existing
                                : typeof existing === "string"
                                  ? [existing]
                                  : [];
                              const nextValue = question.multiSelect
                                ? previous.includes(option.label)
                                  ? previous.filter((value) => value !== option.label)
                                  : [...previous, option.label]
                                : option.label;
                              return {
                                ...current,
                                [question.question]: nextValue,
                              };
                            });
                          }}
                          className={cn("askUserOption", isSelected && "is-selected", !canAnswer && "is-disabled")}
                        >
                          <span className="text-sm font-semibold">{option.label}</span>
                          {option.description ? <span className="text-xs opacity-80">{option.description}</span> : null}
                        </button>
                      );
                    })}
                  </div>
                </div>
              );
            })}
          </div>
        )}

        {bridgeQuestions.length === 0 && options.length > 0 && (
          <div className="askUserOptions flex flex-wrap gap-2">
            {options.map((option) => (
              <button
                key={option.key}
                type="button"
                disabled={!canAnswer}
                onClick={() => toggleOption(option.value)}
                className={cn(
                  "askUserOption",
                  allowMultiple && "is-multiple",
                  isSelected(option.value) && "is-selected",
                  !canAnswer && "is-disabled"
                )}
              >
                <span className="text-sm font-semibold">{option.title}</span>
                {option.description && (
                  <span className="text-xs opacity-80">{option.description}</span>
                )}
              </button>
            ))}
          </div>
        )}

        {liveBridgeRequest && canAnswer && (
          <div className="askUserComposer flex flex-wrap items-end gap-2">
            <Button
              size="sm"
              className="askUserSubmit"
              disabled={!bridgeReady}
              onClick={() => handleSubmit([], "")}
            >
              {submitting ? "Submitting..." : "Submit answers"}
            </Button>
          </div>
        )}

        {!liveBridgeRequest && canAnswer && (allowFreeform || allowMultiple || isConfirm) && (
          <div className="askUserComposer flex flex-wrap items-end gap-2">
            {allowFreeform && (
              <Textarea
                value={freeformValue}
                placeholder={allowMultiple ? "Add a custom answer" : "Type your answer"}
                className="askUserFreeformInput min-h-[40px] flex-1"
                onInput={(e) => setFreeformValue(e.currentTarget.value)}
                rows={allowMultiple ? 2 : 1}
              />
            )}
            {(allowMultiple || isConfirm || allowFreeform) && (
              <Button
                size="sm"
                className="askUserSubmit"
                disabled={!isConfirm && !selectedValues.length && !freeformValue.trim()}
                onClick={() => handleSubmit(selectedValues, freeformValue)}
              >
                {submitting ? "Submitting..." : isConfirm ? "Confirm" : allowMultiple ? "Submit selection" : "Submit answer"}
              </Button>
            )}
          </div>
        )}

        {event.cancelled && (
          <div className="askUserAnswer mt-2 text-sm font-medium text-muted-foreground">
            Cancelled
          </div>
        )}

        {answerText && (
          <div className="askUserAnswer mt-2 text-sm font-medium text-amber-800">
            Answer: {answerText}
          </div>
        )}

        {error && (
          <div className="text-xs text-destructive font-medium">
            {error}
          </div>
        )}

        {awaitingLogSync && !error && (
          <div className="text-xs text-muted-foreground">
            {promptFallbackMode
              ? "Answer sent as a chat reply. Waiting for the agent to continue."
              : "Answer sent. Waiting for the session log to confirm it."}
          </div>
        )}

        {promptFallbackMode && !awaitingLogSync && !error && (
          <div className="text-xs text-muted-foreground">
            Pi RPC could not open this custom question UI, so answering here sends a normal chat reply instead.
          </div>
        )}

        {!resolved && !liveRequest && !usingLegacyFallback && !promptFallbackMode && (
          <div className="text-xs text-muted-foreground">
            This prompt is only available in session history, so reply from the active Pi UI.
          </div>
        )}
      </div>
    </div>
  );
}
