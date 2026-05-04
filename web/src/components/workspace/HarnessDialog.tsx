import { useEffect, useState } from "preact/hooks";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

import { api } from "../../lib/api";

interface HarnessDialogProps {
  open: boolean;
  sessionId: string | null;
  runtimeId?: string | null;
  onClose: () => void;
}

export function HarnessDialog({ open, sessionId, runtimeId = null, onClose }: HarnessDialogProps) {
  const [enabled, setEnabled] = useState(false);
  const [idleAfterMinutes, setIdleAfterMinutes] = useState("5");
  const [maxConsecutiveInjections, setMaxConsecutiveInjections] = useState("10");
  const [goal, setGoal] = useState("");
  const [acceptanceCriteria, setAcceptanceCriteria] = useState("");
  const [contextFilesText, setContextFilesText] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState("");

  useEffect(() => {
    if (!open || !sessionId) {
      return;
    }
    let cancelled = false;
    setLoading(true);
    setStatus("");
    api.getSessionSupervisor(sessionId)
      .then((supervisor) => {
        if (cancelled) return;
        setEnabled(supervisor.enabled === true);
        setIdleAfterMinutes(String(supervisor.idle_after_minutes ?? 5));
        setMaxConsecutiveInjections(String(supervisor.max_consecutive_injections ?? 10));
        setGoal(supervisor.goal || "");
        setAcceptanceCriteria(supervisor.acceptance_criteria || "");
        setContextFilesText(Array.isArray(supervisor.context_files) ? supervisor.context_files.join("\n") : "");
      })
      .catch((error) => {
        if (cancelled) return;
        setStatus(error instanceof Error ? error.message : "Unable to load supervisor settings");
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [open, runtimeId, sessionId]);

  const save = async () => {
    if (!sessionId || saving) {
      return;
    }
    setSaving(true);
    setStatus("Saving...");
    try {
      await api.saveSessionSupervisor(sessionId, {
        enabled,
        idle_after_minutes: Math.max(1, Math.round(Number(idleAfterMinutes) || 1)),
        max_consecutive_injections: Math.max(1, Math.round(Number(maxConsecutiveInjections) || 1)),
        goal,
        acceptance_criteria: acceptanceCriteria,
        context_files: contextFilesText.split(/\r?\n/).map((item) => item.trim()).filter(Boolean),
      });
      setStatus("Saved");
      window.setTimeout(() => {
        onClose();
      }, 150);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Unable to save supervisor settings");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (!nextOpen && !saving) {
        onClose();
      }
    }}>
      <DialogContent className="harnessDialog mobileDetailDialog flex max-h-[88dvh] max-w-2xl flex-col overflow-hidden" titleId="harness-dialog-title">
        <DialogHeader>
          <div className="flex items-start justify-between gap-3">
            <div>
              <DialogTitle id="harness-dialog-title">Supervisor</DialogTitle>
              <p className="text-sm text-muted-foreground">Configure this session's supervisor policy.</p>
            </div>
            <Button type="button" variant="ghost" size="sm" onClick={onClose}>Close</Button>
          </div>
        </DialogHeader>
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-6 pb-6 pt-2">
          <section className="space-y-3 rounded-2xl border border-border/70 bg-background/80 p-4">
            <div>
              <h3 className="text-sm font-semibold text-foreground">Session policy</h3>
              <p className="text-xs text-muted-foreground">Only applies to the active session.</p>
            </div>
            <label className="toggleOption flex items-start gap-3 rounded-2xl border border-border/70 bg-card/80 px-3 py-3 text-sm">
              <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.currentTarget.checked)} />
              <div>
                <strong className="block text-foreground">Enabled</strong>
                <span className="text-muted-foreground">Run supervisor sweeps for this session.</span>
              </div>
            </label>
            <div className="fieldGrid twoCol">
              <label className="fieldBlock">
                <span className="fieldLabel">Idle after minutes</span>
                <Input type="number" min={1} value={idleAfterMinutes} onInput={(event) => setIdleAfterMinutes(event.currentTarget.value)} />
              </label>
              <label className="fieldBlock">
                <span className="fieldLabel">Max consecutive injections</span>
                <Input type="number" min={1} value={maxConsecutiveInjections} onInput={(event) => setMaxConsecutiveInjections(event.currentTarget.value)} />
              </label>
            </div>
            <label className="fieldBlock">
              <span className="fieldLabel">Goal</span>
              <Input value={goal} onInput={(event) => setGoal(event.currentTarget.value)} />
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Acceptance criteria</span>
              <Input value={acceptanceCriteria} onInput={(event) => setAcceptanceCriteria(event.currentTarget.value)} />
            </label>
            <label className="fieldBlock">
              <span className="fieldLabel">Context files</span>
              <Textarea value={contextFilesText} onInput={(event) => setContextFilesText(event.currentTarget.value)} rows={4} placeholder="One path per line" />
            </label>
          </section>

          {loading ? <p className="text-sm text-muted-foreground">Loading supervisor settings...</p> : null}
          {status ? <p className="text-sm text-muted-foreground">{status}</p> : null}
        </div>
        <div className="flex shrink-0 justify-end gap-2 border-t border-border/70 bg-card/95 px-6 py-4">
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="button" onClick={() => void save()} disabled={saving || loading || !sessionId}>Save</Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
