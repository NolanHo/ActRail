import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";

type TeamNodeStatus = "waiting_for_parent" | "running" | "failed" | "idle" | "completed";
type ThreadMessageKind = "leader" | "member" | "system";

interface ThreadMessage {
  id: string;
  kind: ThreadMessageKind;
  label: string;
  body: string;
  ts: string;
  meta?: string;
}

export interface MockTeamNode {
  actorId: string;
  childSessionId: string;
  name: string;
  role: string;
  status: TeamNodeStatus;
  turnId?: string;
  question?: string;
  lastEvent: string;
  model: string;
  cwd: string;
  children: MockTeamNode[];
  messages: ThreadMessage[];
}

export const mockTeamRoots: MockTeamNode[] = [
  {
    actorId: "actor_runtime_lead",
    childSessionId: "session_child_runtime_lead",
    name: "runtime-lead",
    role: "main agent",
    status: "waiting_for_parent",
    turnId: "turn_21",
    question: "Should child sessions stay hidden from the normal Sessions view?",
    lastEvent: "2m ago",
    model: "openai/gpt-5.4",
    cwd: "/root/code/ActRail",
    children: [
      {
        actorId: "actor_reviewer_a7c1",
        childSessionId: "session_child_reviewer_a7c1",
        name: "reviewer",
        role: "code-review",
        status: "waiting_for_parent",
        turnId: "turn_17",
        question: "Should I preserve the deprecated extension API?",
        lastEvent: "2m ago",
        model: "openai/gpt-5.4",
        cwd: "/root/code/ActRail",
        children: [
          {
            actorId: "actor_api_probe_c91a",
            childSessionId: "session_child_api_probe_c91a",
            name: "api-probe",
            role: "contract probe",
            status: "idle",
            turnId: "turn_3",
            lastEvent: "7m ago",
            model: "openai/gpt-5.4-mini",
            cwd: "/root/code/ActRail",
            children: [],
            messages: [
              { id: "m1", kind: "leader", label: "reviewer", body: "Check whether the child session filtering contract conflicts with replay.", ts: "12:03" },
              { id: "m2", kind: "member", label: "api-probe", body: "No conflict if actor APIs read child history by actor id and normal Sessions excludes generated children.", ts: "12:05" },
            ],
          },
        ],
        messages: [
          { id: "m1", kind: "leader", label: "runtime-lead", body: "Review the ActRail subagent runtime plan. Focus on ownership boundaries and failure semantics.", ts: "12:04" },
          { id: "m2", kind: "system", label: "runtime", body: "subagent.turn_started turn_17", ts: "12:04", meta: "actor_reviewer_a7c1" },
          { id: "m3", kind: "member", label: "reviewer", body: "I found one boundary risk: child session history must stay readable without making the child appear in the normal Sessions view.", ts: "12:06" },
          { id: "m4", kind: "member", label: "reviewer", body: "Should I preserve the deprecated extension API?", ts: "12:07", meta: "ask_parent" },
        ],
      },
      {
        actorId: "actor_tester_b2e9",
        childSessionId: "session_child_tester_b2e9",
        name: "tester",
        role: "test-runner",
        status: "running",
        turnId: "turn_9",
        lastEvent: "18s ago",
        model: "openai/gpt-5.4-mini",
        cwd: "/root/code/ActRail",
        children: [],
        messages: [
          { id: "m1", kind: "leader", label: "runtime-lead", body: "Run the actor replay tests after the command API sketch lands.", ts: "12:09" },
          { id: "m2", kind: "member", label: "tester", body: "I am running targeted Go tests for actor replay and question ownership.", ts: "12:10" },
          { id: "m3", kind: "system", label: "runtime", body: "subagent.status running", ts: "12:10", meta: "turn_9" },
        ],
      },
    ],
    messages: [
      { id: "m1", kind: "leader", label: "operator", body: "Build the ActRail-backed subagent runtime shell. Keep generated children out of normal Sessions.", ts: "11:58" },
      { id: "m2", kind: "member", label: "runtime-lead", body: "I split the work into actor identity, command API, replay, ask_parent, and read-only UI packets.", ts: "12:01" },
      { id: "m3", kind: "leader", label: "runtime-lead", body: "Reviewer and tester are working on boundary checks and replay tests.", ts: "12:04" },
      { id: "m4", kind: "member", label: "reviewer", body: "Child session history should be addressable by actor id, but hidden from the normal Sessions list.", ts: "12:06" },
      { id: "m5", kind: "member", label: "tester", body: "Replay tests are running against the actor event cursor model.", ts: "12:10" },
    ],
  },
  {
    actorId: "actor_ui_lead",
    childSessionId: "session_child_ui_lead",
    name: "ui-lead",
    role: "main agent",
    status: "running",
    turnId: "turn_8",
    lastEvent: "1m ago",
    model: "openai/gpt-5.4",
    cwd: "/root/code/ActRail",
    children: [
      {
        actorId: "actor_docs_41fd",
        childSessionId: "session_child_docs_41fd",
        name: "docs-writer",
        role: "documentation",
        status: "idle",
        turnId: "turn_4",
        lastEvent: "11m ago",
        model: "openai/gpt-5.4",
        cwd: "/root/docs/pi-agent/ActRail",
        children: [],
        messages: [
          { id: "m1", kind: "leader", label: "ui-lead", body: "Draft the acceptance criteria for a read-only desktop shell.", ts: "11:52" },
          { id: "m2", kind: "member", label: "docs-writer", body: "Acceptance criteria drafted. The shell has no backend dependency and mobile has no Subagents entrypoint.", ts: "11:59" },
        ],
      },
      {
        actorId: "actor_probe_0d55",
        childSessionId: "session_child_probe_0d55",
        name: "runtime-probe",
        role: "diagnostics",
        status: "failed",
        turnId: "turn_2",
        lastEvent: "24m ago",
        model: "openai/gpt-5.4-mini",
        cwd: "/root/code/ActRail",
        children: [],
        messages: [
          { id: "m1", kind: "leader", label: "ui-lead", body: "Check whether the frontend can bootstrap through the validation port.", ts: "11:41" },
          { id: "m2", kind: "member", label: "runtime-probe", body: "The static server returns 404 for API routes. Use Vite proxy or an edge proxy for validation.", ts: "11:43" },
        ],
      },
    ],
    messages: [
      { id: "m1", kind: "leader", label: "operator", body: "Make the subagent UI shell. Mobile does not need this function.", ts: "11:45" },
      { id: "m2", kind: "member", label: "ui-lead", body: "I moved global switching into a dedicated left rail and made the right pane an IM-style team flow.", ts: "11:51" },
      { id: "m3", kind: "member", label: "docs-writer", body: "The worklog records desktop-only scope and mock-data boundaries.", ts: "11:59" },
      { id: "m4", kind: "member", label: "runtime-probe", body: "Validation needs Vite proxy on the frontend port because the static server does not proxy API routes.", ts: "12:02" },
    ],
  },
];

export const mockSubagents = mockTeamRoots;

const statusRank: Record<TeamNodeStatus, number> = {
  waiting_for_parent: 0,
  running: 1,
  failed: 2,
  idle: 3,
  completed: 4,
};

const statusLabel: Record<TeamNodeStatus, string> = {
  waiting_for_parent: "waiting",
  running: "running",
  failed: "failed",
  idle: "idle",
  completed: "completed",
};

function descendantCount(node: MockTeamNode): number {
  return node.children.reduce((sum, child) => sum + 1 + descendantCount(child), 0);
}

function visibleTeamNodes(nodes: MockTeamNode[]) {
  return nodes
    .filter((node) => node.children.length > 0)
    .slice()
    .sort((a, b) => statusRank[a.status] - statusRank[b.status] || a.name.localeCompare(b.name));
}

function findNode(nodes: MockTeamNode[], actorId: string): MockTeamNode | null {
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

interface SubagentsRailProps {
  selectedActorId: string;
  onSelect(actorId: string): void;
}

export function SubagentsRail({ selectedActorId, onSelect }: SubagentsRailProps) {
  const nodes = visibleTeamNodes(mockTeamRoots);
  const selectedVisible = nodes.some((node) => node.actorId === selectedActorId) ? selectedActorId : nodes[0]?.actorId;

  return (
    <div className="subagentsRailShell">
      <section className="subagentsRailHeader" aria-label="Subagent team filters">
        <p className="sessionsEyebrow">Team leads</p>
        <h2 className="sessionsSurfaceTitle">Subagents</h2>
        <div className="subagentsRailStats" aria-label="Subagent counts">
          <span>{nodes.length} non-leaf agents</span>
          <span>{nodes.reduce((sum, node) => sum + descendantCount(node), 0)} descendants</span>
        </div>
        <input className="subagentsSearch" type="search" placeholder="Search team leads" aria-label="Search team leads" />
        <div className="subagentsFilterRow" aria-label="Mock filters">
          <Badge variant="outline">status</Badge>
          <Badge variant="outline">team</Badge>
        </div>
      </section>
      <ScrollArea className="subagentsRailBody">
        <div className="mainAgentList">
          {nodes.map((node) => (
            <button
              key={node.actorId}
              type="button"
              className={cn("mainAgentSelectCard", selectedVisible === node.actorId && "active")}
              aria-current={selectedVisible === node.actorId ? "true" : undefined}
              onClick={() => onSelect(node.actorId)}
            >
              <span className="mainAgentCardTopline">
                <strong>{node.name}</strong>
                <span className={cn("subagentStatusPill", node.status)}>{statusLabel[node.status]}</span>
              </span>
              <span className="mainAgentCardMeta">{node.role}</span>
              <span className="mainAgentCardBadges">
                <span>{node.children.length} direct</span>
                <span>{descendantCount(node)} total</span>
              </span>
            </button>
          ))}
        </div>
      </ScrollArea>
    </div>
  );
}

interface SubagentsThreadViewProps {
  selectedActorId: string;
}

export function SubagentsThreadView({ selectedActorId }: SubagentsThreadViewProps) {
  const selected = findNode(mockTeamRoots, selectedActorId) || visibleTeamNodes(mockTeamRoots)[0] || mockTeamRoots[0];
  const team = selected.children.slice().sort((a, b) => statusRank[a.status] - statusRank[b.status] || a.name.localeCompare(b.name));

  return (
    <section className="subagentsThreadView" aria-label="Subagent team conversation view">
      <header className="subagentsThreadHeader">
        <div className="subagentsThreadTitleBlock">
          <p className="sessionsEyebrow">Selected team lead</p>
          <h1>{selected.name}</h1>
          <p>{selected.role} / {selected.cwd}</p>
        </div>
        <div className="subagentsThreadHeaderMeta">
          <span className={cn("subagentStatusPill", selected.status)}>{statusLabel[selected.status]}</span>
          <span>{team.length} direct reports</span>
          <Button type="button" variant="outline" disabled>Details later</Button>
        </div>
      </header>

      <div className="subagentsTeamStrip" aria-label="Direct subagents">
        {team.map((node) => (
          <article key={node.actorId} className="subagentTeamChip">
            <div className="subagentCardTopline">
              <strong>{node.name}</strong>
              <span className={cn("subagentStatusPill", node.status)}>{statusLabel[node.status]}</span>
            </div>
            <span>{node.role}</span>
            {node.children.length ? <small>{node.children.length} child agents</small> : null}
          </article>
        ))}
      </div>

      <ScrollArea className="subagentsThreadScroll">
        <div className="subagentsThreadMessages">
          {selected.messages.map((message) => (
            <article key={message.id} className={cn("subagentsThreadMessage", message.kind)}>
              <div className="subagentsMessageMeta">
                <span>{message.label}</span>
                <time>{message.ts}</time>
              </div>
              <p>{message.body}</p>
              {message.meta ? <small>{message.meta}</small> : null}
            </article>
          ))}
        </div>
      </ScrollArea>

      <footer className="subagentsThreadFooter">
        <span>Left rail hides leaf agents. A subagent appears there only when it leads its own children.</span>
        <span>{selected.actorId}</span>
      </footer>
    </section>
  );
}
