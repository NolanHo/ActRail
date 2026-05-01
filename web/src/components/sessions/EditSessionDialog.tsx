import { useEffect, useMemo, useState } from "preact/hooks";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";

import { api } from "../../lib/api";
import { getSessionDisplayName } from "../../lib/session-display";
import type { SessionSummary } from "../../lib/types";

interface EditSessionDialogProps {
  open: boolean;
  session: SessionSummary | null;
  sessions: SessionSummary[];
  onClose: () => void;
  onSaved: () => Promise<void> | void;
}

type SnoozeMode = "none" | "4h" | "tomorrow" | "custom";

function sessionLabel(session: SessionSummary) {
  return getSessionDisplayName(session);
}

function formatPriorityOffset(value: number) {
  const prefix = value >= 0 ? "+" : "";
  return `${prefix}${value.toFixed(2)}`;
}

function tomorrowSnoozeSeconds() {
  const date = new Date();
  date.setDate(date.getDate() + 1);
  date.setHours(9, 0, 0, 0);
  return Math.floor(date.getTime() / 1000);
}

function fillCustomSnoozeFields(tsSeconds: number) {
  const ts = Number(tsSeconds);
  const date = Number.isFinite(ts) && ts > 0 ? new Date(ts * 1000) : new Date(Date.now() + 24 * 3600 * 1000);
  const yyyy = String(date.getFullYear()).padStart(4, "0");
  const mm = String(date.getMonth() + 1).padStart(2, "0");
  const dd = String(date.getDate()).padStart(2, "0");
  const hh = String(date.getHours()).padStart(2, "0");
  const mi = String(date.getMinutes()).padStart(2, "0");
  return {
    customDate: `${yyyy}-${mm}-${dd}`,
    customTime: `${hh}:${mi}`,
  };
}

function initialFormState(session: SessionSummary | null) {
  const snoozeUntil = Number(session?.snooze_until || 0);
  const nextCustom = fillCustomSnoozeFields(snoozeUntil > Date.now() / 1000 ? snoozeUntil : tomorrowSnoozeSeconds());
  return {
    sessionName: String(session?.alias || ""),
    priorityOffset: Number(session?.priority_offset || 0),
    snoozeMode: (snoozeUntil > Date.now() / 1000 ? "custom" : "none") as SnoozeMode,
    customDate: nextCustom.customDate,
    customTime: nextCustom.customTime,
    dependencySessionId: String(session?.dependency_session_id || ""),
  };
}

export function EditSessionDialog({ open, session, sessions, onClose, onSaved }: EditSessionDialogProps) {
  const [sessionName, setSessionName] = useState(() => initialFormState(session).sessionName);
  const [priorityOffset, setPriorityOffset] = useState(() => initialFormState(session).priorityOffset);
  const [snoozeMode, setSnoozeMode] = useState<SnoozeMode>(() => initialFormState(session).snoozeMode);
  const [customDate, setCustomDate] = useState(() => initialFormState(session).customDate);
  const [customTime, setCustomTime] = useState(() => initialFormState(session).customTime);
  const [dependencySessionId, setDependencySessionId] = useState(() => initialFormState(session).dependencySessionId);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const dependencyOptions = useMemo(
    () => sessions.filter((item) => item.session_id !== session?.session_id),
    [session?.session_id, sessions],
  );

  useEffect(() => {
    if (!open) return;
    const next = initialFormState(session);
    setSessionName(next.sessionName);
    setPriorityOffset(next.priorityOffset);
    setSnoozeMode(next.snoozeMode);
    setCustomDate(next.customDate);
    setCustomTime(next.customTime);
    setDependencySessionId(next.dependencySessionId);
    setError("");
  }, [open, session?.session_id]);

  if (!open || !session) {
    return null;
  }

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (!nextOpen && !saving) {
        onClose();
      }
    }}>
      <DialogContent titleId="edit-session-dialog-title" className="flex max-h-[88dvh] max-w-2xl flex-col overflow-hidden border-border/70 bg-card/95 p-0 shadow-2xl shadow-primary/10">
        <DialogHeader className="space-y-3 p-6 pb-5">
          <div className="space-y-1">
            <DialogTitle id="edit-session-dialog-title">Edit conversation</DialogTitle>
            <p className="text-sm text-muted-foreground">Edit queue metadata for this session.</p>
          </div>
        </DialogHeader>

        <Separator className="bg-border/70" />

        <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
          <label className="block space-y-2">
            <span className="text-sm font-medium text-foreground">Conversation name</span>
            <Input
              name="sessionName"
              value={sessionName}
              maxLength={80}
              placeholder={sessionLabel(session)}
              onInput={(event) => setSessionName(event.currentTarget.value)}
              onChange={(event) => setSessionName(event.currentTarget.value)}
            />
          </label>

          <div className="space-y-3 rounded-2xl border border-border/70 bg-background/70 p-4">
            <div className="flex items-center justify-between gap-3">
              <span className="text-sm font-medium text-foreground">Priority offset</span>
              <span className="text-sm font-medium text-muted-foreground">{formatPriorityOffset(priorityOffset)}</span>
            </div>
            <div className="flex items-center gap-3">
              <input
                name="priorityOffset"
                type="range"
                min="-5"
                max="5"
                step="0.25"
                value={priorityOffset}
                className="w-full accent-primary"
                onInput={(event) => setPriorityOffset(Number(event.currentTarget.value))}
              />
            </div>
            <p className="text-xs text-muted-foreground">Positive values rank this session higher in the sidebar; negative values push it down.</p>
          </div>

          <div className="space-y-3 rounded-2xl border border-border/70 bg-background/70 p-4">
            <div>
              <span className="text-sm font-medium text-foreground">Snooze</span>
              <p className="text-xs text-muted-foreground">Hide this session until a later time.</p>
            </div>
            <div className="grid gap-2 sm:grid-cols-4">
              {([
                ["none", "None"],
                ["4h", "4 hours"],
                ["tomorrow", "Tomorrow 9:00"],
                ["custom", "Custom"],
              ] as const).map(([value, label]) => (
                <label key={value} className="flex items-center gap-2 rounded-xl border border-border/60 bg-card/60 px-3 py-2 text-sm">
                  <input
                    type="radio"
                    name="snoozeMode"
                    checked={snoozeMode === value}
                    onChange={() => setSnoozeMode(value)}
                  />
                  <span>{label}</span>
                </label>
              ))}
            </div>
            {snoozeMode === "custom" ? (
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="block space-y-2">
                  <span className="text-xs font-medium text-muted-foreground">Date</span>
                  <Input type="date" value={customDate} onInput={(event) => setCustomDate(event.currentTarget.value)} />
                </label>
                <label className="block space-y-2">
                  <span className="text-xs font-medium text-muted-foreground">Time</span>
                  <Input type="time" value={customTime} onInput={(event) => setCustomTime(event.currentTarget.value)} />
                </label>
              </div>
            ) : null}
          </div>

          <label className="block space-y-2">
            <span className="text-sm font-medium text-foreground">Depends on session</span>
            <select
              name="dependencySessionId"
              className="w-full rounded-xl border border-input bg-background px-3 py-2 text-sm"
              value={dependencySessionId}
              onChange={(event) => setDependencySessionId(event.currentTarget.value)}
            >
              <option value="">No dependency</option>
              {dependencyOptions.map((item) => (
                <option key={item.session_id} value={item.session_id}>{sessionLabel(item)}</option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">Dependent sessions stay behind their blocker in the sidebar.</p>
          </label>

          {error ? <p className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</p> : null}
        </div>

        <div className="flex shrink-0 justify-end gap-3 border-t border-border/70 bg-card/95 px-6 py-4">
          <Button type="button" variant="outline" disabled={saving} onClick={onClose}>Cancel</Button>
          <Button
            type="button"
            disabled={saving}
            onClick={async () => {
              let snoozeUntil = 0;
              if (snoozeMode === "4h") {
                snoozeUntil = Math.floor(Date.now() / 1000) + 4 * 3600;
              } else if (snoozeMode === "tomorrow") {
                snoozeUntil = tomorrowSnoozeSeconds();
              } else if (snoozeMode === "custom") {
                const parsed = Date.parse(`${customDate}T${customTime}`);
                if (!Number.isFinite(parsed)) {
                  setError("Invalid snooze time.");
                  return;
                }
                snoozeUntil = Math.floor(parsed / 1000);
              }

              setSaving(true);
              setError("");
              try {
                await api.editSession(session.session_id, {
                  name: sessionName,
                  priority_offset: Number(priorityOffset.toFixed(2)),
                  snooze_until: snoozeUntil,
                  dependency_session_id: dependencySessionId || null,
                });
                await onSaved();
                onClose();
              } catch (saveError) {
                setError(saveError instanceof Error ? saveError.message : "Failed to update session");
              } finally {
                setSaving(false);
              }
            }}
          >
            {saving ? "Saving..." : "Save changes"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
