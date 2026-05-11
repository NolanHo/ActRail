import { useEffect, useState } from "preact/hooks";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

import { api } from "../../lib/api";
import { getSessionDisplayName } from "../../lib/session-display";
import type { SessionSummary, SessionSupervisorSnapshot, SupervisorRunSummary } from "../../lib/types";

interface SessionSupervisorDialogProps {
  open: boolean;
  session: SessionSummary | null;
  onClose(): void;
  onSaved?(): Promise<void> | void;
}

function numericDraft(value: string, fallback: number, min: number) {
  const parsed = Math.round(Number(value));
  if (!Number.isFinite(parsed)) return fallback;
  return Math.max(min, parsed);
}

function formatTS(ts: number | undefined) {
  if (!ts) return "";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(ts * 1000));
}

export function SessionSupervisorDialog({ open, session, onClose, onSaved }: SessionSupervisorDialogProps) {
  const [config, setConfig] = useState<SessionSupervisorSnapshot | null>(null);
  const [runs, setRuns] = useState<SupervisorRunSummary[]>([]);
  const [enabled, setEnabled] = useState(false);
  const [idleMinutes, setIdleMinutes] = useState("5");
  const [maxInjections, setMaxInjections] = useState("10");
  const [goal, setGoal] = useState("");
  const [acceptance, setAcceptance] = useState("");
  const [contextFiles, setContextFiles] = useState("");
  const [status, setStatus] = useState("");

  const sessionId = session?.session_id || "";

  const load = async () => {
    if (!sessionId) {
      return;
    }
    setStatus("Loading supervisor...");
    try {
      const [nextConfig, nextRuns] = await Promise.all([
        api.getSessionSupervisor(sessionId),
        api.getSupervisorRuns(sessionId, 20),
      ]);
      setConfig(nextConfig);
      setEnabled(nextConfig.enabled === true);
      setIdleMinutes(String(nextConfig.idle_after_minutes ?? 5));
      setMaxInjections(String(nextConfig.max_consecutive_injections ?? 10));
      setGoal(nextConfig.goal || "");
      setAcceptance(nextConfig.acceptance_criteria || "");
      setContextFiles(Array.isArray(nextConfig.context_files) ? nextConfig.context_files.join("\n") : "");
      setRuns(nextRuns.runs || []);
      setStatus("");
    } catch (error) {
      setConfig(null);
      setRuns([]);
      setStatus(error instanceof Error ? error.message : "Unable to load supervisor");
    }
  };

  useEffect(() => {
    if (open && sessionId) {
      void load();
    }
  }, [open, sessionId]);

  const save = async () => {
    if (!sessionId) return;
    setStatus("Saving...");
    try {
      const response = await api.saveSessionSupervisor(sessionId, {
        enabled,
        idle_after_minutes: numericDraft(idleMinutes, 5, 1),
        max_consecutive_injections: numericDraft(maxInjections, 10, 1),
        goal,
        acceptance_criteria: acceptance,
        context_files: contextFiles.split(/\r?\n/).map((item) => item.trim()).filter(Boolean),
      });
      setConfig(response);
      setStatus("Saved");
      await onSaved?.();
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Unable to save supervisor");
    }
  };

  const run = async (dryRun: boolean) => {
    if (!sessionId) return;
    setStatus(dryRun ? "Running dry run..." : "Running supervisor...");
    try {
      await api.runSupervisorOnce(sessionId, dryRun);
      await load();
      setStatus(dryRun ? "Dry run recorded" : "Run requested");
      await onSaved?.();
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Unable to run supervisor");
    }
  };

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onClose(); }}>
      <DialogContent className="max-w-4xl">
        <DialogHeader>
          <DialogTitle>Session supervisor</DialogTitle>
        </DialogHeader>
        <div className="space-y-5 px-6 pb-6 pt-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <p className="text-sm font-medium text-foreground">{session ? getSessionDisplayName(session) : ""}</p>
              <p className="text-sm text-muted-foreground">{session?.agent_backend || ""}</p>
            </div>
            <span className="rounded-md border border-border/70 px-2 py-1 text-xs text-muted-foreground">
              {config ? `${config.status} ${config.consecutive_injections}/${config.max_consecutive_injections}` : "Not loaded"}
            </span>
          </div>

          <div className="fieldGrid threeCol">
            <label className="toggleOption flex items-start gap-3 rounded-md border border-border/70 bg-card/80 px-3 py-3 text-sm">
              <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.currentTarget.checked)} />
              <span className="text-foreground">Enabled</span>
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Idle after minutes</span>
              <Input type="number" min={1} value={idleMinutes} onInput={(event) => setIdleMinutes(event.currentTarget.value)} />
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Max consecutive injections</span>
              <Input type="number" min={1} value={maxInjections} onInput={(event) => setMaxInjections(event.currentTarget.value)} />
            </label>
          </div>

          <div className="fieldGrid twoCol">
            <label className="fieldBlock">
              <span className="fieldLabel">Goal</span>
              <Input value={goal} onInput={(event) => setGoal(event.currentTarget.value)} />
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Acceptance criteria</span>
              <Input value={acceptance} onInput={(event) => setAcceptance(event.currentTarget.value)} />
            </label>
          </div>

          <label className="fieldBlock">
            <span className="fieldLabel">Context files</span>
            <Textarea value={contextFiles} rows={3} onInput={(event) => setContextFiles(event.currentTarget.value)} placeholder="One path per line" />
          </label>

          <div className="flex flex-wrap gap-2">
            <Button type="button" data-testid="session-supervisor-save" onClick={() => void save()} disabled={!sessionId}>Save</Button>
            <Button type="button" data-testid="session-supervisor-dry-run" variant="outline" onClick={() => void run(true)} disabled={!sessionId}>Dry run</Button>
            <Button type="button" data-testid="session-supervisor-run-now" onClick={() => void run(false)} disabled={!sessionId || enabled !== true}>Run now</Button>
            <Button type="button" variant="outline" onClick={() => void load()} disabled={!sessionId}>Refresh</Button>
          </div>

          {status ? <p className="text-sm text-muted-foreground">{status}</p> : null}

          <section className="space-y-3">
            <h3 className="text-sm font-semibold text-foreground">Supervisor runs</h3>
            {runs.length ? (
              <div className="workspaceTableWrap">
                <table className="workspaceTable">
                  <thead><tr><th>Created</th><th>Status</th><th>Action</th><th>Reason</th></tr></thead>
                  <tbody>
                    {runs.map((runItem) => (
                      <tr key={runItem.run_id}>
                        <td>{formatTS(runItem.created_ts)}</td>
                        <td>{runItem.status}</td>
                        <td>{runItem.action || ""}</td>
                        <td>{runItem.reason || runItem.error || runItem.injected_text || runItem.run_id}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : <p className="text-sm text-muted-foreground">No supervisor runs.</p>}
          </section>
        </div>
      </DialogContent>
    </Dialog>
  );
}
