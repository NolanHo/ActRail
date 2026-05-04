import { useEffect, useState } from "preact/hooks";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";

import { api } from "../../lib/api";
import type { InboxItem } from "../../lib/types";

interface InboxDialogProps {
  open: boolean;
  sessionId: string | null;
  onClose(): void;
}

function formatDue(ts: number | undefined) {
  if (!ts) return "";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(ts * 1000));
}

export function InboxDialog({ open, sessionId, onClose }: InboxDialogProps) {
  const [items, setItems] = useState<InboxItem[]>([]);
  const [status, setStatus] = useState("");

  useEffect(() => {
    if (!open || !sessionId) return;
    let cancelled = false;
    setStatus("Loading inbox...");
    api.getSessionInbox(sessionId, 100)
      .then((response) => {
        if (cancelled) return;
        setItems(response.items || []);
        setStatus("");
      })
      .catch((error) => {
        if (cancelled) return;
        setStatus(error instanceof Error ? error.message : "Unable to load inbox");
      });
    return () => {
      cancelled = true;
    };
  }, [open, sessionId]);

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (!nextOpen) onClose();
    }}>
      <DialogContent className="mobileDetailDialog max-w-3xl" titleId="inbox-dialog-title">
        <DialogHeader>
          <div className="flex items-start justify-between gap-3">
            <div>
              <DialogTitle id="inbox-dialog-title">Inbox</DialogTitle>
              <p className="text-sm text-muted-foreground">Pending and delivered scheduler messages for the active session.</p>
            </div>
            <Button type="button" variant="ghost" size="sm" onClick={onClose}>Close</Button>
          </div>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto p-5">
          {items.length ? (
            <div className="workspaceTableWrap">
              <table className="workspaceTable">
                <thead><tr><th>Due</th><th>Source</th><th>State</th><th>Title</th><th>Message</th></tr></thead>
                <tbody>
                  {items.map((item) => (
                    <tr key={item.item_id}>
                      <td>{formatDue(item.due_ts)}</td>
                      <td>{item.source}</td>
                      <td>{item.state}</td>
                      <td>{item.title || item.item_id}</td>
                      <td>{item.message || item.blocked_reason || item.error}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : <p className="text-sm text-muted-foreground">No inbox items.</p>}
          {status ? <p className="text-sm text-muted-foreground">{status}</p> : null}
        </div>
      </DialogContent>
    </Dialog>
  );
}
