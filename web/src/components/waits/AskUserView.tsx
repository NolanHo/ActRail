import { useEffect, useMemo } from "preact/hooks";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useSessionsStoreApi, useWaitsStore, useWaitsStoreApi } from "../../app/providers";
import type { ActiveWaitSummary } from "../../lib/types";
import { WaitStateBadge } from "./WaitStateBadge";
import { WaitThreadPanel } from "./WaitThreadPanel";

function newestActiveWait(waits: ActiveWaitSummary[]) {
  return [...waits].sort((a, b) => (b.created_at ?? b.updated_at ?? 0) - (a.created_at ?? a.updated_at ?? 0))[0] ?? null;
}

export function AskUserView() {
  const waitsState = useWaitsStore();
  const waitsStore = useWaitsStoreApi();
  const sessionsStore = useSessionsStoreApi();
  useEffect(() => {
    void waitsStore.loadInbox();
  }, [waitsStore]);
  const selectedWait = useMemo(() => {
    const selectedSessionId = Object.entries(waitsState.selectedThreadBySessionId).find(([, threadId]) => Boolean(threadId))?.[0] ?? "";
    if (selectedSessionId) {
      const wait = waitsState.activeBySessionId[selectedSessionId];
      if (wait) {
        return wait;
      }
    }
    return newestActiveWait(waitsState.inbox);
  }, [waitsState.activeBySessionId, waitsState.inbox, waitsState.selectedThreadBySessionId]);

  return (
    <section className="askUserView" aria-label="AskUser waits">
      <aside className="askUserInboxPane">
        <div className="waitInboxHeader">
          <div>
            <p className="sectionEyebrow">Runtime waits</p>
            <h2>AskUser</h2>
          </div>
          <Button type="button" size="sm" variant="outline" onClick={() => void waitsStore.loadInbox()}>Refresh</Button>
        </div>
        {waitsState.error ? <p className="text-sm text-destructive">{waitsState.error}</p> : null}
        <ScrollArea className="askUserInboxList">
          {waitsState.inbox.length ? waitsState.inbox.map((wait) => {
            const selected = selectedWait?.wait_id === wait.wait_id;
            return (
              <button
                key={wait.wait_id}
                type="button"
                className={`waitInboxItem askUserInboxItem${selected ? " selected" : ""}`}
                onClick={() => {
                  if (wait.session_id) {
                    sessionsStore.select(wait.session_id);
                  }
                  waitsStore.openWait(wait);
                }}
              >
                <span className="waitInboxItemTopline">
                  <WaitStateBadge state={wait.state} />
                  {wait.session_id ? <span className="font-mono text-xs text-muted-foreground">{wait.session_id}</span> : null}
                </span>
                <strong>{wait.question}</strong>
                {wait.blocking_reason ? <span>{wait.blocking_reason}</span> : null}
              </button>
            );
          }) : (
            <div className="askUserEmpty">
              <h3>No active AskUser waits</h3>
              <p>Runtime questions appear here when an agent blocks on user input.</p>
            </div>
          )}
        </ScrollArea>
      </aside>
      <main className="askUserThreadPane">
        {selectedWait ? (
          <WaitThreadPanel sessionId={selectedWait.session_id ?? null} activeWait={selectedWait} />
        ) : (
          <div className="askUserEmpty askUserEmptyMain">
            <h3>No wait selected</h3>
            <p>Select an active wait to claim, answer, cancel, or inspect terminal state.</p>
          </div>
        )}
      </main>
    </section>
  );
}
