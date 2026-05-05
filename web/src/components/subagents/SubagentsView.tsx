import { useEffect, useMemo, useState } from "preact/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import type { SubagentMessage, SubagentNode } from "@/lib/types";

type TeamNodeStatus = "waiting_for_parent" | "running" | "failed" | "idle" | "completed" | "aborted" | "closed" | string;
type ThreadMessageKind = "leader" | "member" | "system" | string;

export interface TeamNode {
  actorId: string;
  childSessionId: string;
  parentSessionId: string;
  name: string;
  role: string;
  status: TeamNodeStatus;
  turnId?: string;
  question?: string;
  lastEvent: string;
  lastEventTs?: number;
  model: string;
  cwd: string;
  children: TeamNode[];
  messages: ThreadMessage[];
}

interface ThreadMessage {
  id: string;
  kind: ThreadMessageKind;
  label: string;
  body: string;
  ts: string;
  meta?: string;
}

export interface SubagentsData {
  roots: TeamNode[];
  totalCount: number;
  nonLeafCount: number;
  loading: boolean;
  error: string;
  refresh(): void;
}

const statusRank: Record<string, number> = {
  waiting_for_parent: 0,
  running: 1,
  failed: 2,
  idle: 3,
  completed: 4,
  aborted: 5,
  closed: 6,
};

const statusLabel: Record<string, string> = {
  waiting_for_parent: "waiting",
  running: "running",
  failed: "failed",
  idle: "idle",
  completed: "completed",
  aborted: "aborted",
  closed: "closed",
};

function normalizeStatus(status: string | undefined): TeamNodeStatus {
  const cleaned = status?.trim();
  return cleaned || "idle";
}

function sortRank(status: TeamNodeStatus): number {
  return statusRank[status] ?? 99;
}

function formatStatus(status: TeamNodeStatus): string {
  return statusLabel[status] ?? status.split("_").join(" ");
}

function formatEventTime(value: number | undefined): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) {
    return "no events";
  }
  const ageMs = Date.now() - value * 1000;
  if (!Number.isFinite(ageMs) || ageMs < 0) {
    return new Date(value * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }
  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (ageMs < minute) {
    return "just now";
  }
  if (ageMs < hour) {
    return `${Math.floor(ageMs / minute)}m ago`;
  }
  if (ageMs < day) {
    return `${Math.floor(ageMs / hour)}h ago`;
  }
  return `${Math.floor(ageMs / day)}d ago`;
}

function formatMessageTime(value: number | undefined): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) {
    return "";
  }
  return new Date(value * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function normalizeMessage(message: SubagentMessage, index: number): ThreadMessage {
  return {
    id: message.message_id || `message_${index}`,
    kind: message.kind || "system",
    label: message.label || "runtime",
    body: message.body || "",
    ts: formatMessageTime(message.ts),
    meta: message.meta,
  };
}

function normalizeNode(node: SubagentNode): TeamNode {
  const children = (node.children ?? []).map(normalizeNode);
  const status = normalizeStatus(node.status);
  const lastEventTs = node.last_event_ts;
  return {
    actorId: node.actor_id,
    childSessionId: node.child_session_id,
    parentSessionId: node.parent_session_id,
    name: node.name || node.actor_id,
    role: node.role || "subagent",
    status,
    turnId: node.turn_id,
    question: node.question,
    lastEvent: formatEventTime(lastEventTs),
    lastEventTs,
    model: node.model || "",
    cwd: node.cwd || "",
    children,
    messages: (node.messages ?? []).map(normalizeMessage),
  };
}

function descendantCount(node: TeamNode): number {
  return node.children.reduce((sum, child) => sum + 1 + descendantCount(child), 0);
}

function visibleTeamNodes(nodes: TeamNode[]) {
  return nodes
    .filter((node) => node.children.length > 0)
    .slice()
    .sort((a, b) => sortRank(a.status) - sortRank(b.status) || a.name.localeCompare(b.name));
}

function allTeamNodes(nodes: TeamNode[]): TeamNode[] {
  return nodes.flatMap((node) => [node, ...allTeamNodes(node.children)]);
}

function findNode(nodes: TeamNode[], actorId: string): TeamNode | null {
  for (const node of nodes) {
    if (node.actorId === actorId) {
      return node;
    }
    const child = findNode(node.children, actorId);
    if (child) {
      return child;
    }
  }
  return null;
}

function selectableNodes(nodes: TeamNode[]) {
  const nonLeaf = visibleTeamNodes(nodes);
  return nonLeaf.length ? nonLeaf : allTeamNodes(nodes).sort((a, b) => sortRank(a.status) - sortRank(b.status) || a.name.localeCompare(b.name));
}

export function useSubagentsData(refreshMs = 5000): SubagentsData {
  const [roots, setRoots] = useState<TeamNode[]>([]);
  const [totalCount, setTotalCount] = useState(0);
  const [nonLeafCount, setNonLeafCount] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [refreshToken, setRefreshToken] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    let cancelled = false;
    const load = async () => {
      setLoading(true);
      try {
        const response = await api.listSubagents({ includeClosed: true }, controller.signal);
        if (cancelled) {
          return;
        }
        const nextRoots = (response.roots ?? []).map(normalizeNode);
        setRoots(nextRoots);
        setTotalCount(response.total_count ?? allTeamNodes(nextRoots).length);
        setNonLeafCount(response.non_leaf_count ?? visibleTeamNodes(nextRoots).length);
        setError("");
      } catch (err) {
        if (!controller.signal.aborted) {
          setError(err instanceof Error ? err.message : String(err));
        }
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    };
    void load();
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [refreshToken]);

  useEffect(() => {
    if (refreshMs <= 0) {
      return;
    }
    const id = window.setInterval(() => setRefreshToken((value) => value + 1), refreshMs);
    return () => window.clearInterval(id);
  }, [refreshMs]);

  return {
    roots,
    totalCount,
    nonLeafCount,
    loading,
    error,
    refresh: () => setRefreshToken((value) => value + 1),
  };
}

interface SubagentsRailProps {
  selectedActorId: string;
  data: SubagentsData;
  onSelect(actorId: string): void;
}

export function SubagentsRail({ selectedActorId, data, onSelect }: SubagentsRailProps) {
  const nodes = useMemo(() => selectableNodes(data.roots), [data.roots]);
  const selectedVisible = nodes.some((node) => node.actorId === selectedActorId) ? selectedActorId : nodes[0]?.actorId;

  useEffect(() => {
    if (selectedVisible && selectedVisible !== selectedActorId) {
      onSelect(selectedVisible);
    }
  }, [onSelect, selectedActorId, selectedVisible]);

  return (
    <div className="subagentsRailShell">
      <section className="subagentsRailHeader" aria-label="Team filters">
        <p className="sessionsEyebrow">Teams</p>
        <h2 className="sessionsSurfaceTitle">Teams</h2>
        <div className="subagentsRailStats" aria-label="Team counts">
          <span>{data.nonLeafCount} team leads</span>
          <span>{data.totalCount} total</span>
        </div>
        <input className="subagentsSearch" type="search" placeholder="Search teams" aria-label="Search teams" disabled />
        <div className="subagentsFilterRow" aria-label="Live filters">
          <Badge variant="outline">live</Badge>
          <Badge variant="outline">{data.loading ? "loading" : "synced"}</Badge>
        </div>
        {data.error ? <p className="text-xs text-destructive">{data.error}</p> : null}
      </section>
      <ScrollArea className="subagentsRailBody">
        <div className="mainAgentList">
          {nodes.length ? nodes.map((node) => (
            <button
              key={node.actorId}
              type="button"
              className={cn("mainAgentSelectCard", selectedVisible === node.actorId && "active")}
              aria-current={selectedVisible === node.actorId ? "true" : undefined}
              onClick={() => onSelect(node.actorId)}
            >
              <span className="mainAgentCardTopline">
                <strong>{node.name}</strong>
                <span className={cn("subagentStatusPill", node.status)}>{formatStatus(node.status)}</span>
              </span>
              <span className="mainAgentCardMeta">{node.role}</span>
              <span className="mainAgentCardBadges">
                <span>{node.children.length} direct</span>
                <span>{descendantCount(node)} descendants</span>
              </span>
            </button>
          )) : (
            <p className="text-sm text-muted-foreground">No live teams.</p>
          )}
        </div>
      </ScrollArea>
    </div>
  );
}

interface SubagentsThreadViewProps {
  selectedActorId: string;
  data: SubagentsData;
}

export function SubagentsThreadView({ selectedActorId, data }: SubagentsThreadViewProps) {
  const selected = findNode(data.roots, selectedActorId) || selectableNodes(data.roots)[0] || data.roots[0];
  const team = selected?.children.slice().sort((a, b) => sortRank(a.status) - sortRank(b.status) || a.name.localeCompare(b.name)) ?? [];

  if (!selected) {
    return (
      <section className="subagentsThreadView" aria-label="Team conversation view">
        <header className="subagentsThreadHeader">
          <div className="subagentsThreadTitleBlock">
            <p className="sessionsEyebrow">Selected team</p>
            <h1>Teams</h1>
            <p>{data.loading ? "Loading live team state" : "No live teams"}</p>
          </div>
          <div className="subagentsThreadHeaderMeta">
            <Button type="button" variant="outline" onClick={data.refresh}>Refresh</Button>
          </div>
        </header>
      </section>
    );
  }

  return (
    <section className="subagentsThreadView" aria-label="Team conversation view">
      <header className="subagentsThreadHeader">
        <div className="subagentsThreadTitleBlock">
          <p className="sessionsEyebrow">Selected team</p>
          <h1>{selected.name}</h1>
          <p>{selected.role} / {selected.cwd || "unknown cwd"}</p>
        </div>
        <div className="subagentsThreadHeaderMeta">
          <span className={cn("subagentStatusPill", selected.status)}>{formatStatus(selected.status)}</span>
          <span>{team.length} direct reports</span>
          <Button type="button" variant="outline" onClick={data.refresh}>Refresh</Button>
        </div>
      </header>

      <div className="subagentsTeamStrip" aria-label="Team members">
        {team.length ? team.map((node) => (
          <article key={node.actorId} className="subagentTeamChip">
            <div className="subagentCardTopline">
              <strong>{node.name}</strong>
              <span className={cn("subagentStatusPill", node.status)}>{formatStatus(node.status)}</span>
            </div>
            <span>{node.role}</span>
            {node.children.length ? <small>{node.children.length} nested teams</small> : <small>{node.lastEvent}</small>}
          </article>
        )) : <p className="text-sm text-muted-foreground">No direct team members.</p>}
      </div>

      <ScrollArea className="subagentsThreadScroll">
        <div className="subagentsThreadMessages">
          {selected.messages.length ? selected.messages.map((message) => (
            <article key={message.id} className={cn("subagentsThreadMessage", message.kind)}>
              <div className="subagentsMessageMeta">
                <span>{message.label}</span>
                <time>{message.ts}</time>
              </div>
              <p>{message.body}</p>
              {message.meta ? <small>{message.meta}</small> : null}
            </article>
          )) : (
            <p className="text-sm text-muted-foreground">No recorded team messages.</p>
          )}
        </div>
      </ScrollArea>

      <footer className="subagentsThreadFooter">
        <span>{selected.lastEvent}</span>
        <span>{selected.actorId}</span>
      </footer>
    </section>
  );
}
