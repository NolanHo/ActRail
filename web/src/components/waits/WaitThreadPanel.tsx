import { useEffect, useMemo, useState } from "preact/hooks";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useSessionsStore, useWaitsStore, useWaitsStoreApi } from "../../app/providers";
import type { ActiveWaitSummary, WaitRecord } from "../../lib/types";
import { getSessionRuntimeId } from "../../lib/session-identity";
import { WaitAnswerForm } from "./WaitAnswerForm";
import { WaitJustification } from "./WaitJustification";
import { WaitStateBadge } from "./WaitStateBadge";

function newestWait(waits: WaitRecord[]) {
  return [...waits].sort((a, b) => (b.created_at ?? b.updated_at ?? 0) - (a.created_at ?? a.updated_at ?? 0))[0] ?? null;
}

function terminalSummary(wait: WaitRecord | ActiveWaitSummary) {
  if (wait.state === "timed_out_locked" && "fallback_used" in wait && wait.fallback_used) {
    return { label: "Fallback used", value: wait.fallback_used };
  }
  if (wait.state === "answered" && "answer" in wait && wait.answer) {
    return { label: "Answer", value: wait.answer };
  }
  if (wait.state === "cancelled") {
    return { label: "Terminal state", value: "This wait was cancelled." };
  }
  if (wait.state === "orphaned") {
    return { label: "Terminal state", value: "This wait was orphaned because the runtime connection ended." };
  }
  return null;
}

interface WaitThreadPanelProps {
  sessionId?: string | null;
  runtimeId?: string | null;
  activeWait?: ActiveWaitSummary | null;
}

export function WaitThreadPanel({ sessionId: sessionIdProp, runtimeId: runtimeIdProp, activeWait: activeWaitProp }: WaitThreadPanelProps) {
  const sessionsState = useSessionsStore();
  const waitsState = useWaitsStore();
  const waitsStore = useWaitsStoreApi();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const activeSessionId = sessionIdProp ?? sessionsState.activeSessionId;
  const activeSession = activeSessionId ? sessionsState.items.find((item) => item.session_id === activeSessionId) ?? null : null;
  const runtimeId = runtimeIdProp ?? getSessionRuntimeId(activeSession);
  const storeActiveWait = activeSessionId ? waitsState.activeBySessionId[activeSessionId] ?? null : null;
  const activeWait = activeWaitProp ?? storeActiveWait ?? activeSession?.active_wait ?? null;
  const selectedThreadId = activeSessionId ? waitsState.selectedThreadBySessionId[activeSessionId] || activeWait?.thread_id || null : null;
  const waits = selectedThreadId ? waitsState.waitsByThreadId[selectedThreadId] ?? [] : [];
  const current = useMemo(() => newestWait(waits) ?? activeWait, [activeWait, waits]);
  const active = current?.state === "pending_unread" || current?.state === "claimed";
  const terminal = current ? terminalSummary(current) : null;

  useEffect(() => {
    if (!activeSessionId || !selectedThreadId) {
      return;
    }
    void waitsStore.loadThread(activeSessionId, selectedThreadId, runtimeId);
  }, [activeSessionId, runtimeId, selectedThreadId, waitsStore]);

  const runAction = async (action: () => Promise<void>) => {
    setSubmitting(true);
    setError("");
    try {
      await action();
    } catch (err) {
      setError(err instanceof Error ? err.message : "wait action failed");
    } finally {
      setSubmitting(false);
    }
  };

  if (!activeSessionId) {
    return <p className="text-sm text-muted-foreground">Select a session to inspect waits.</p>;
  }
  if (!current) {
    return <p className="text-sm text-muted-foreground">No wait selected for this session.</p>;
  }

  return (
    <ScrollArea className="workspaceScroll h-full pr-1">
      <div className="waitThreadPanel">
        <div className="waitPanelHeader">
          <WaitStateBadge state={current.state} />
          <div>
            <h3>{current.question}</h3>
            {current.timeout_at && active ? <p>Timeout: {new Date(current.timeout_at * 1000).toLocaleString()}</p> : null}
          </div>
        </div>
        {"context" in current && current.context ? (
          <section className="waitPanelSection">
            <h4>Context</h4>
            <p>{current.context}</p>
          </section>
        ) : null}
        <section className="waitPanelSection">
          <h4>Justification</h4>
          <WaitJustification wait={current} />
        </section>
        {terminal ? (
          <section className="waitPanelSection">
            <h4>{terminal.label}</h4>
            <p>{terminal.value}</p>
          </section>
        ) : null}
        {current.state === "pending_unread" ? (
          <div className="waitPanelActions">
            <Button type="button" disabled={submitting} onClick={() => void runAction(() => waitsStore.claimWait(activeSessionId, current.wait_id, runtimeId))}>Claim</Button>
            <Button type="button" variant="outline" disabled={submitting} onClick={() => void runAction(() => waitsStore.cancelWait(activeSessionId, current.wait_id, runtimeId))}>Cancel wait</Button>
          </div>
        ) : null}
        {current.state === "claimed" ? (
          <div className="waitPanelActionsVertical">
            <WaitAnswerForm
              disabled={submitting}
              submitting={submitting}
              onSubmit={(answer) => void runAction(() => waitsStore.answerWait(activeSessionId, current.wait_id, answer, runtimeId))}
            />
            <Button type="button" variant="outline" disabled={submitting} onClick={() => void runAction(() => waitsStore.cancelWait(activeSessionId, current.wait_id, runtimeId))}>Cancel wait</Button>
          </div>
        ) : null}
        {error ? <p className="text-sm font-medium text-destructive">{error}</p> : null}
        {waits.length > 1 ? (
          <section className="waitPanelSection">
            <h4>Thread history</h4>
            <div className="waitHistoryList">
              {waits.map((wait) => (
                <article key={wait.wait_id} className="waitHistoryItem">
                  <WaitStateBadge state={wait.state} />
                  <strong>{wait.question}</strong>
                </article>
              ))}
            </div>
          </section>
        ) : null}
      </div>
    </ScrollArea>
  );
}
