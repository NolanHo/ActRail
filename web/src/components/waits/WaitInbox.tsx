import { useEffect } from "preact/hooks";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useSessionsStoreApi, useWaitsStore, useWaitsStoreApi } from "../../app/providers";
import { WaitStateBadge } from "./WaitStateBadge";

export function WaitInbox() {
  const waitsState = useWaitsStore();
  const waitsStore = useWaitsStoreApi();
  const sessionsStore = useSessionsStoreApi();

  useEffect(() => {
    void waitsStore.loadInbox();
  }, [waitsStore]);

  if (!waitsState.inbox.length) {
    return (
      <section className="waitInbox empty">
        <div className="waitInboxHeader">
          <h3>Waiting Inbox</h3>
          <Button type="button" size="sm" variant="outline" onClick={() => void waitsStore.loadInbox()}>Refresh</Button>
        </div>
        {waitsState.error ? <p className="text-destructive">{waitsState.error}</p> : <p>No active durable waits.</p>}
      </section>
    );
  }

  return (
    <section className="waitInbox">
      <div className="waitInboxHeader">
        <h3>Waiting Inbox</h3>
        <Button type="button" size="sm" variant="outline" onClick={() => void waitsStore.loadInbox()}>Refresh</Button>
      </div>
      {waitsState.error ? <p className="text-sm text-destructive">{waitsState.error}</p> : null}
      <ScrollArea className="waitInboxList">
        {waitsState.inbox.map((wait) => (
          <button
            key={wait.wait_id}
            type="button"
            className="waitInboxItem"
            onClick={() => {
              if (wait.session_id) {
                sessionsStore.select(wait.session_id);
                waitsStore.openWait(wait);
              }
            }}
          >
            <span className="waitInboxItemTopline">
              <WaitStateBadge state={wait.state} />
              {wait.session_id ? <span className="font-mono text-xs text-muted-foreground">{wait.session_id}</span> : null}
            </span>
            <strong>{wait.question}</strong>
            {wait.blocking_reason ? <span>{wait.blocking_reason}</span> : null}
          </button>
        ))}
      </ScrollArea>
    </section>
  );
}
