import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { ActiveWaitSummary, MessageEvent } from "../../lib/types";
import { normalizeActiveWait } from "../../domains/waits/normalize";
import { WaitJustification } from "./WaitJustification";
import { WaitStateBadge } from "./WaitStateBadge";

interface WaitCardProps {
  event?: MessageEvent;
  wait?: ActiveWaitSummary | null;
  sessionId?: string;
  onOpen?: (wait: ActiveWaitSummary) => void;
  onClaim?: (wait: ActiveWaitSummary) => void;
  onCancel?: (wait: ActiveWaitSummary) => void;
}

export function waitFromMessageEvent(event: MessageEvent, sessionId?: string): ActiveWaitSummary | null {
  return normalizeActiveWait(event.wait ?? event, sessionId);
}

export function WaitCard({ event, wait: waitProp, sessionId, onOpen, onClaim, onCancel }: WaitCardProps) {
  const wait = waitProp ?? (event ? waitFromMessageEvent(event, sessionId) : null);
  if (!wait) {
    return null;
  }
  const active = wait.state === "pending_unread" || wait.state === "claimed";
  return (
    <article data-testid="message-surface" data-kind="wait" className={cn("messageCard waitCard", !active && "resolved")}>
      <div className="waitCardHeader">
        <WaitStateBadge state={wait.state} />
        {wait.timeout_at && active ? <span className="waitCardMeta">timeout {new Date(wait.timeout_at * 1000).toLocaleTimeString()}</span> : null}
      </div>
      <div className="waitCardQuestion">{wait.question}</div>
      <WaitJustification wait={wait} />
      <div className="waitCardActions">
        {onOpen ? <Button type="button" size="sm" variant="outline" onClick={() => onOpen(wait)}>Open wait</Button> : null}
        {wait.state === "pending_unread" && onClaim ? <Button type="button" size="sm" onClick={() => onClaim(wait)}>Claim</Button> : null}
        {active && onCancel ? <Button type="button" size="sm" variant="outline" onClick={() => onCancel(wait)}>Cancel wait</Button> : null}
      </div>
    </article>
  );
}
