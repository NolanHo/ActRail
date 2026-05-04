import { useEffect, useState } from "preact/hooks";

import { api } from "../../lib/api";
import type { InboxItem, SchedulerItem, SchedulerSettings } from "../../lib/types";

function formatDue(ts: number | undefined) {
  if (!ts) return "";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(ts * 1000));
}

interface SchedulerSnapshot {
  settings: SchedulerSettings | null;
  items: SchedulerItem[];
  inbox: InboxItem[];
}

export function SchedulerView() {
  const [snapshot, setSnapshot] = useState<SchedulerSnapshot>({ settings: null, items: [], inbox: [] });
  const [status, setStatus] = useState("Loading scheduler...");

  useEffect(() => {
    let cancelled = false;
    api.getScheduler(100)
      .then((response) => {
        if (cancelled) return;
        setSnapshot({ settings: response.settings, items: response.items || [], inbox: response.inbox || [] });
        setStatus("");
      })
      .catch((error) => {
        if (cancelled) return;
        setStatus(error instanceof Error ? error.message : "Unable to load scheduler");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <section className="subagentsThreadView" aria-label="Scheduler view">
      <header className="subagentsThreadHeader">
        <div>
          <p className="sectionEyebrow">Global view</p>
          <h2>Scheduler</h2>
          <p>Alarms, supervisor preset activity, and inbox delivery state across sessions.</p>
        </div>
      </header>
      <div className="subagentsThreadBody">
        <section className="workspaceCard">
          <h3>Settings</h3>
          <dl className="workspaceMetaGrid">
            <div>
              <dt>Idle before delivery</dt>
              <dd>{snapshot.settings?.idle_before_delivery_seconds ?? 30}s</dd>
            </div>
          </dl>
        </section>

        <section className="workspaceCard">
          <h3>Scheduled items</h3>
          {snapshot.items.length ? (
            <div className="workspaceTableWrap">
              <table className="workspaceTable">
                <thead><tr><th>Due</th><th>Kind</th><th>Session</th><th>State</th><th>Title</th></tr></thead>
                <tbody>
                  {snapshot.items.map((item) => (
                    <tr key={item.item_id}>
                      <td>{formatDue(item.due_ts)}</td>
                      <td>{item.kind}</td>
                      <td>{item.session_id}</td>
                      <td>{item.state}</td>
                      <td>{item.title || item.message || item.item_id}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : <p className="text-sm text-muted-foreground">No scheduled items.</p>}
        </section>

        <section className="workspaceCard">
          <h3>Inbox timeline</h3>
          {snapshot.inbox.length ? (
            <div className="workspaceTableWrap">
              <table className="workspaceTable">
                <thead><tr><th>Due</th><th>Source</th><th>Session</th><th>State</th><th>Title</th></tr></thead>
                <tbody>
                  {snapshot.inbox.map((item) => (
                    <tr key={item.item_id}>
                      <td>{formatDue(item.due_ts)}</td>
                      <td>{item.source}</td>
                      <td>{item.session_id}</td>
                      <td>{item.state}</td>
                      <td>{item.title || item.message || item.item_id}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : <p className="text-sm text-muted-foreground">No inbox items.</p>}
        </section>
        {status ? <p className="text-sm text-muted-foreground">{status}</p> : null}
      </div>
    </section>
  );
}
