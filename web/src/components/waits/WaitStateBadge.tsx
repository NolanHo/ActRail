import { Badge } from "@/components/ui/badge";
import { waitStateLabel } from "../../domains/waits/normalize";

export function WaitStateBadge({ state }: { state?: string }) {
  const variant = state === "pending_unread" || state === "claimed" ? "default" : "outline";
  return <Badge variant={variant}>{waitStateLabel(state)}</Badge>;
}
