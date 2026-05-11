import { useCallback, useEffect, useState } from "preact/hooks";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

import { api } from "../../lib/api";
import type {
  InboxItem,
  SchedulerItem,
  SchedulerSettings,
  SessionSummary,
  SupervisorProviderResponse,
} from "../../lib/types";

function formatDue(ts: number | undefined) {
  if (!ts) return "";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(ts * 1000));
}

function sessionLabel(session: SessionSummary) {
  return session.alias || session.display_name || session.title || session.first_user_message || session.session_id;
}

function sessionBackendLabel(session: SessionSummary | undefined) {
  if (!session) return "";
  return session.agent_backend ? `${sessionLabel(session)} (${session.agent_backend})` : sessionLabel(session);
}

function numericDraft(value: string, fallback: number, min: number) {
  const parsed = Math.round(Number(value));
  if (!Number.isFinite(parsed)) return fallback;
  return Math.max(min, parsed);
}

interface SchedulerSnapshot {
  settings: SchedulerSettings | null;
  items: SchedulerItem[];
  inbox: InboxItem[];
}

export function SchedulerView() {
  const [snapshot, setSnapshot] = useState<SchedulerSnapshot>({ settings: null, items: [], inbox: [] });
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [selectedReminderSessionId, setSelectedReminderSessionId] = useState("");
  const [status, setStatus] = useState("Loading scheduler...");
  const [schedulerStatus, setSchedulerStatus] = useState("");
  const [settingsDraft, setSettingsDraft] = useState("30");
  const [reminderTitle, setReminderTitle] = useState("Self Reminder");
  const [reminderMessage, setReminderMessage] = useState("");
  const [reminderDelaySeconds, setReminderDelaySeconds] = useState("60");
  const [reminderStatus, setReminderStatus] = useState("");
  const [provider, setProvider] = useState<SupervisorProviderResponse | null>(null);
  const [providerBaseUrl, setProviderBaseUrl] = useState("");
  const [providerModel, setProviderModel] = useState("");
  const [providerApiKey, setProviderApiKey] = useState("");
  const [providerStatus, setProviderStatus] = useState("");

  const loadScheduler = useCallback(async () => {
    const response = await api.getScheduler(100);
    setSnapshot({ settings: response.settings, items: response.items || [], inbox: response.inbox || [] });
    setSettingsDraft(String(response.settings?.idle_before_delivery_seconds ?? 30));
  }, []);

  const loadSessions = useCallback(async () => {
    const response = await api.listSessions({ limit: 100 }, undefined, false);
    const nextSessions = response.items || response.sessions || [];
    setSessions(nextSessions);
    setSelectedReminderSessionId((current) => {
      if (current && nextSessions.some((session) => session.session_id === current)) return current;
      return nextSessions[0]?.session_id || "";
    });
  }, []);

  const loadProvider = useCallback(async () => {
    const response = await api.getSupervisorProvider();
    setProvider(response);
    setProviderBaseUrl(response.base_url || "");
    setProviderModel(response.model || "");
    setProviderApiKey("");
  }, []);

  const loadAll = useCallback(async () => {
    setStatus("Loading scheduler...");
    try {
      await Promise.all([loadScheduler(), loadSessions(), loadProvider()]);
      setStatus("");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Unable to load scheduler");
    }
  }, [loadProvider, loadScheduler, loadSessions]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  const saveSchedulerSettings = async () => {
    setSchedulerStatus("Saving...");
    try {
      const idle = numericDraft(settingsDraft, 30, 0);
      const response = await api.saveSchedulerSettings({ idle_before_delivery_seconds: idle });
      setSettingsDraft(String(response.idle_before_delivery_seconds ?? idle));
      await loadScheduler();
      setSchedulerStatus("Saved");
    } catch (error) {
      setSchedulerStatus(error instanceof Error ? error.message : "Unable to save scheduler settings");
    }
  };

  const createSelfReminder = async () => {
    if (!selectedReminderSessionId) {
      setReminderStatus("Select a session");
      return;
    }
    if (!reminderMessage.trim()) {
      setReminderStatus("Message required");
      return;
    }
    setReminderStatus("Creating self-reminder...");
    try {
      await api.createSelfReminder({
        session_id: selectedReminderSessionId,
        duration_seconds: numericDraft(reminderDelaySeconds, 0, 0),
        title: reminderTitle,
        message: reminderMessage,
      });
      setReminderMessage("");
      await loadScheduler();
      setReminderStatus("Self-reminder scheduled");
    } catch (error) {
      setReminderStatus(error instanceof Error ? error.message : "Unable to create self-reminder");
    }
  };

  const saveProvider = async () => {
    setProviderStatus("Saving...");
    try {
      const response = await api.saveSupervisorProvider({
        base_url: providerBaseUrl,
        model: providerModel,
        ...(providerApiKey.trim() ? { api_key: providerApiKey.trim() } : {}),
      });
      setProvider(response);
      setProviderBaseUrl(response.base_url || "");
      setProviderModel(response.model || "");
      setProviderApiKey("");
      setProviderStatus(response.complete ? "Provider ready" : "Provider incomplete");
    } catch (error) {
      setProviderStatus(error instanceof Error ? error.message : "Unable to save provider");
    }
  };

  const testProvider = async () => {
    setProviderStatus("Testing provider...");
    try {
      const response = await api.testSupervisorProvider({
        base_url: providerBaseUrl,
        model: providerModel,
        ...(providerApiKey.trim() ? { api_key: providerApiKey.trim() } : {}),
      });
      setProviderStatus(response.output ? `Test passed: ${response.output}` : "Test passed");
    } catch (error) {
      setProviderStatus(error instanceof Error ? error.message : "Unable to test provider");
    }
  };

  return (
    <section className="teamsThreadView" aria-label="Scheduler view">
      <header className="teamsThreadHeader">
        <div>
          <p className="sectionEyebrow">Global view</p>
          <h2>Scheduler</h2>
          <p>Self-reminders, supervisor preset activity, and inbox delivery state across sessions.</p>
        </div>
        <Button type="button" variant="outline" size="sm" onClick={() => void loadAll()}>Refresh</Button>
      </header>
      <div className="teamsThreadBody">
        <section className="workspaceCard space-y-4 p-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3>Scheduler controls</h3>
              <p className="text-sm text-muted-foreground">Delivery waits until a session has been idle for this many seconds.</p>
            </div>
            <dl className="workspaceMetaGrid min-w-44">
              <div>
                <dt>Current idle</dt>
                <dd>{snapshot.settings?.idle_before_delivery_seconds ?? 30}s</dd>
              </div>
            </dl>
          </div>
          <div className="fieldGrid threeCol">
            <label className="fieldBlock">
              <span className="fieldLabel">Idle before delivery</span>
              <Input type="number" min={0} value={settingsDraft} onInput={(event) => setSettingsDraft(event.currentTarget.value)} />
            </label>
            <div />
            <Button type="button" data-testid="scheduler-settings-save" onClick={() => void saveSchedulerSettings()}>Save</Button>
          </div>
          {schedulerStatus ? <p className="text-sm text-muted-foreground">{schedulerStatus}</p> : null}
        </section>

        <section className="workspaceCard space-y-4 p-4">
          <div>
            <h3>Create self-reminder</h3>
            <p className="text-sm text-muted-foreground">Self-reminders are staged into inbox at due time and delivered when the session is idle.</p>
          </div>
          <div className="fieldGrid threeCol">
            <label className="fieldBlock">
              <span className="fieldLabel">Session</span>
              <select value={selectedReminderSessionId} onChange={(event) => setSelectedReminderSessionId(event.currentTarget.value)}>
                {sessions.map((session) => <option key={session.session_id} value={session.session_id}>{sessionBackendLabel(session)}</option>)}
              </select>
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Due in seconds</span>
              <Input type="number" min={0} value={reminderDelaySeconds} onInput={(event) => setReminderDelaySeconds(event.currentTarget.value)} />
            </label>
            <Button type="button" data-testid="scheduler-self-reminder-create" onClick={() => void createSelfReminder()} disabled={!selectedReminderSessionId}>Create</Button>
          </div>
          <div className="fieldGrid twoCol">
            <label className="fieldBlock">
              <span className="fieldLabel">Title</span>
              <Input value={reminderTitle} onInput={(event) => setReminderTitle(event.currentTarget.value)} />
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Message</span>
              <Input value={reminderMessage} onInput={(event) => setReminderMessage(event.currentTarget.value)} />
            </label>
          </div>
          {reminderStatus ? <p className="text-sm text-muted-foreground">{reminderStatus}</p> : null}
        </section>

        <section className="workspaceCard space-y-4 p-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3>Supervisor provider</h3>
              <p className="text-sm text-muted-foreground">OpenAI-compatible endpoint used by automatic and manual supervisor runs.</p>
            </div>
            <span className="rounded-md border border-border/70 px-2 py-1 text-xs text-muted-foreground">
              {provider?.complete ? "Ready" : provider?.api_key_configured ? "Incomplete" : "API key missing"}
            </span>
          </div>
          <div className="fieldGrid threeCol">
            <label className="fieldBlock">
              <span className="fieldLabel">Base URL</span>
              <Input value={providerBaseUrl} onInput={(event) => setProviderBaseUrl(event.currentTarget.value)} />
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Model</span>
              <Input value={providerModel} onInput={(event) => setProviderModel(event.currentTarget.value)} />
            </label>
            <div className="flex flex-wrap gap-2">
              <Button type="button" data-testid="supervisor-provider-save" onClick={() => void saveProvider()}>Save</Button>
              <Button type="button" data-testid="supervisor-provider-test" variant="outline" onClick={() => void testProvider()}>Test hello</Button>
            </div>
          </div>
          <label className="fieldBlock">
            <span className="fieldLabel">API key</span>
            <Input type="password" value={providerApiKey} placeholder={provider?.api_key_configured ? "Leave blank to keep saved key" : ""} onInput={(event) => setProviderApiKey(event.currentTarget.value)} />
          </label>
          {providerStatus ? <p className="text-sm text-muted-foreground">{providerStatus}</p> : null}
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
