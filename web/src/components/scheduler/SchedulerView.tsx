import { useCallback, useEffect, useMemo, useState } from "preact/hooks";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

import { api } from "../../lib/api";
import type {
  InboxItem,
  SchedulerItem,
  SchedulerSettings,
  SessionSummary,
  SessionSupervisorSnapshot,
  SupervisorProviderResponse,
  SupervisorRunSummary,
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
  const [selectedSupervisorSessionId, setSelectedSupervisorSessionId] = useState("");
  const [status, setStatus] = useState("Loading inbox...");
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
  const [supervisor, setSupervisor] = useState<SessionSupervisorSnapshot | null>(null);
  const [supervisorEnabled, setSupervisorEnabled] = useState(false);
  const [supervisorIdleMinutes, setSupervisorIdleMinutes] = useState("5");
  const [supervisorMaxInjections, setSupervisorMaxInjections] = useState("10");
  const [supervisorGoal, setSupervisorGoal] = useState("");
  const [supervisorAcceptance, setSupervisorAcceptance] = useState("");
  const [supervisorContextFiles, setSupervisorContextFiles] = useState("");
  const [supervisorRuns, setSupervisorRuns] = useState<SupervisorRunSummary[]>([]);
  const [supervisorStatus, setSupervisorStatus] = useState("");

  const selectedSupervisorSession = useMemo(() => sessions.find((session) => session.session_id === selectedSupervisorSessionId), [selectedSupervisorSessionId, sessions]);
  const piSessions = useMemo(() => sessions.filter((session) => session.agent_backend === "pi"), [sessions]);

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
    setSelectedSupervisorSessionId((current) => {
      if (current && nextSessions.some((session) => session.session_id === current && session.agent_backend === "pi")) return current;
      return nextSessions.find((session) => session.agent_backend === "pi")?.session_id || "";
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
    setStatus("Loading inbox...");
    try {
      await Promise.all([loadScheduler(), loadSessions(), loadProvider()]);
      setStatus("");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Unable to load inbox");
    }
  }, [loadProvider, loadScheduler, loadSessions]);

  const loadSessionSupervisor = useCallback(async (sessionId: string) => {
    if (!sessionId) {
      setSupervisor(null);
      setSupervisorRuns([]);
      return;
    }
    setSupervisorStatus("Loading supervisor...");
    try {
      const [config, runs] = await Promise.all([
        api.getSessionSupervisor(sessionId),
        api.getSupervisorRuns(sessionId, 20),
      ]);
      setSupervisor(config);
      setSupervisorEnabled(config.enabled === true);
      setSupervisorIdleMinutes(String(config.idle_after_minutes ?? 5));
      setSupervisorMaxInjections(String(config.max_consecutive_injections ?? 10));
      setSupervisorGoal(config.goal || "");
      setSupervisorAcceptance(config.acceptance_criteria || "");
      setSupervisorContextFiles(Array.isArray(config.context_files) ? config.context_files.join("\n") : "");
      setSupervisorRuns(runs.runs || []);
      setSupervisorStatus("");
    } catch (error) {
      setSupervisor(null);
      setSupervisorRuns([]);
      setSupervisorStatus(error instanceof Error ? error.message : "Unable to load supervisor");
    }
  }, []);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  useEffect(() => {
    void loadSessionSupervisor(selectedSupervisorSessionId);
  }, [loadSessionSupervisor, selectedSupervisorSessionId]);

  const saveSchedulerSettings = async () => {
    setSchedulerStatus("Saving...");
    try {
      const idle = numericDraft(settingsDraft, 30, 0);
      const response = await api.saveSchedulerSettings({ idle_before_delivery_seconds: idle });
      setSettingsDraft(String(response.idle_before_delivery_seconds ?? idle));
      await loadScheduler();
      setSchedulerStatus("Saved");
    } catch (error) {
      setSchedulerStatus(error instanceof Error ? error.message : "Unable to save delivery settings");
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

  const saveSessionSupervisor = async () => {
    if (!selectedSupervisorSessionId) {
      setSupervisorStatus("Select a session");
      return;
    }
    setSupervisorStatus("Saving...");
    try {
      const response = await api.saveSessionSupervisor(selectedSupervisorSessionId, {
        enabled: supervisorEnabled,
        idle_after_minutes: numericDraft(supervisorIdleMinutes, 5, 1),
        max_consecutive_injections: numericDraft(supervisorMaxInjections, 10, 1),
        goal: supervisorGoal,
        acceptance_criteria: supervisorAcceptance,
        context_files: supervisorContextFiles.split(/\r?\n/).map((item) => item.trim()).filter(Boolean),
      });
      setSupervisor(response);
      setSupervisorStatus("Saved");
      await loadSessions();
    } catch (error) {
      setSupervisorStatus(error instanceof Error ? error.message : "Unable to save supervisor");
    }
  };

  const runSupervisor = async (dryRun: boolean) => {
    if (!selectedSupervisorSessionId) {
      setSupervisorStatus("Select a session");
      return;
    }
    setSupervisorStatus(dryRun ? "Running dry run..." : "Running supervisor...");
    try {
      await api.runSupervisorOnce(selectedSupervisorSessionId, dryRun);
      await loadSessionSupervisor(selectedSupervisorSessionId);
      setSupervisorStatus(dryRun ? "Dry run recorded" : "Run requested");
    } catch (error) {
      setSupervisorStatus(error instanceof Error ? error.message : "Unable to run supervisor");
    }
  };

  return (
    <section className="teamsThreadView" aria-label="Inbox view">
      <header className="teamsThreadHeader">
        <div>
          <p className="sectionEyebrow">Global view</p>
          <h2>Inbox</h2>
          <p>Manual follow-ups, self-reminders, supervisor activity, and delivery state across sessions.</p>
        </div>
        <Button type="button" variant="outline" size="sm" onClick={() => void loadAll()}>Refresh</Button>
      </header>
      <div className="teamsThreadBody">
        <section className="workspaceCard space-y-4 p-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3>Delivery controls</h3>
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
            <p className="text-sm text-muted-foreground">Self-reminders enter the session inbox at due time and deliver when the session is idle.</p>
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
            <Button type="button" data-testid="supervisor-provider-save" onClick={() => void saveProvider()}>Save</Button>
          </div>
          <label className="fieldBlock">
            <span className="fieldLabel">API key</span>
            <Input type="password" value={providerApiKey} placeholder={provider?.api_key_configured ? "Leave blank to keep saved key" : ""} onInput={(event) => setProviderApiKey(event.currentTarget.value)} />
          </label>
          {providerStatus ? <p className="text-sm text-muted-foreground">{providerStatus}</p> : null}
        </section>

        <section className="workspaceCard space-y-4 p-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3>Session supervisor</h3>
              <p className="text-sm text-muted-foreground">{piSessions.length ? `${piSessions.length} Pi sessions available` : "Supervisor runs only on Pi sessions."}</p>
            </div>
            <span className="rounded-md border border-border/70 px-2 py-1 text-xs text-muted-foreground">
              {supervisor ? `${supervisor.status} ${supervisor.consecutive_injections}/${supervisor.max_consecutive_injections}` : selectedSupervisorSession ? selectedSupervisorSession.agent_backend : "No Pi session"}
            </span>
          </div>
          <div className="fieldGrid threeCol">
            <label className="fieldBlock">
              <span className="fieldLabel">Pi session</span>
              <select value={selectedSupervisorSessionId} onChange={(event) => setSelectedSupervisorSessionId(event.currentTarget.value)}>
                {piSessions.map((session) => <option key={session.session_id} value={session.session_id}>{sessionLabel(session)}</option>)}
              </select>
            </label>
            <label className="toggleOption flex items-start gap-3 rounded-md border border-border/70 bg-card/80 px-3 py-3 text-sm">
              <input type="checkbox" checked={supervisorEnabled} onChange={(event) => setSupervisorEnabled(event.currentTarget.checked)} />
              <span className="text-foreground">Enabled</span>
            </label>
            <Button type="button" data-testid="session-supervisor-save" onClick={() => void saveSessionSupervisor()} disabled={!selectedSupervisorSessionId}>Save</Button>
          </div>
          <div className="fieldGrid twoCol">
            <label className="fieldBlock">
              <span className="fieldLabel">Idle after minutes</span>
              <Input type="number" min={1} value={supervisorIdleMinutes} onInput={(event) => setSupervisorIdleMinutes(event.currentTarget.value)} />
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Max consecutive injections</span>
              <Input type="number" min={1} value={supervisorMaxInjections} onInput={(event) => setSupervisorMaxInjections(event.currentTarget.value)} />
            </label>
          </div>
          <div className="fieldGrid twoCol">
            <label className="fieldBlock">
              <span className="fieldLabel">Goal</span>
              <Input value={supervisorGoal} onInput={(event) => setSupervisorGoal(event.currentTarget.value)} />
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Acceptance criteria</span>
              <Input value={supervisorAcceptance} onInput={(event) => setSupervisorAcceptance(event.currentTarget.value)} />
            </label>
          </div>
          <label className="fieldBlock">
            <span className="fieldLabel">Context files</span>
            <Textarea value={supervisorContextFiles} rows={3} onInput={(event) => setSupervisorContextFiles(event.currentTarget.value)} placeholder="One path per line" />
          </label>
          <div className="flex flex-wrap gap-2">
            <Button type="button" data-testid="session-supervisor-dry-run" variant="outline" onClick={() => void runSupervisor(true)} disabled={!selectedSupervisorSessionId}>Dry run</Button>
            <Button type="button" data-testid="session-supervisor-run-now" onClick={() => void runSupervisor(false)} disabled={!selectedSupervisorSessionId || supervisorEnabled !== true}>Run now</Button>
          </div>
          {supervisorStatus ? <p className="text-sm text-muted-foreground">{supervisorStatus}</p> : null}
        </section>

        <section className="workspaceCard">
          <h3>Scheduled reminders</h3>
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
          <h3>Inbox</h3>
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

        <section className="workspaceCard">
          <h3>Supervisor runs</h3>
          {supervisorRuns.length ? (
            <div className="workspaceTableWrap">
              <table className="workspaceTable">
                <thead><tr><th>Created</th><th>Status</th><th>Action</th><th>Reason</th></tr></thead>
                <tbody>
                  {supervisorRuns.map((run) => (
                    <tr key={run.run_id}>
                      <td>{formatDue(run.created_ts)}</td>
                      <td>{run.status}</td>
                      <td>{run.action || ""}</td>
                      <td>{run.reason || run.error || run.injected_text || run.run_id}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : <p className="text-sm text-muted-foreground">No supervisor runs.</p>}
        </section>

        {status ? <p className="text-sm text-muted-foreground">{status}</p> : null}
      </div>
    </section>
  );
}
