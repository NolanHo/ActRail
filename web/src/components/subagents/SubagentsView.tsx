import { useMemo } from "preact/hooks";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";

type SubagentStatus = "waiting_for_parent" | "running" | "failed" | "idle" | "completed";

export interface MockSubagent {
  actorId: string;
  childSessionId: string;
  parentSessionId: string;
  parentTitle: string;
  name: string;
  role: string;
  status: SubagentStatus;
  turnId?: string;
  question?: string;
  questionContext?: string;
  lastEvent: string;
  lastOutput: string;
  model: string;
  cwd: string;
  eventCount: number;
}

export const mockSubagents: MockSubagent[] = [
  {
    actorId: "actor_reviewer_a7c1",
    childSessionId: "session_child_reviewer_a7c1",
    parentSessionId: "parent_session_actrail_runtime",
    parentTitle: "ActRail subagent runtime",
    name: "reviewer",
    role: "code-review",
    status: "waiting_for_parent",
    turnId: "turn_17",
    question: "Should I preserve the deprecated extension API?",
    questionContext: "The code path is internal, but the docs still mention the old extension contract.",
    lastEvent: "2m ago",
    lastOutput: "Found one boundary leak in the parent-child registry path.",
    model: "openai/gpt-5.4",
    cwd: "/root/code/ActRail",
    eventCount: 28,
  },
  {
    actorId: "actor_tester_b2e9",
    childSessionId: "session_child_tester_b2e9",
    parentSessionId: "parent_session_actrail_runtime",
    parentTitle: "ActRail subagent runtime",
    name: "tester",
    role: "test-runner",
    status: "running",
    turnId: "turn_9",
    lastEvent: "18s ago",
    lastOutput: "Running targeted Go tests for actor replay and question ownership.",
    model: "openai/gpt-5.4-mini",
    cwd: "/root/code/ActRail",
    eventCount: 41,
  },
  {
    actorId: "actor_docs_41fd",
    childSessionId: "session_child_docs_41fd",
    parentSessionId: "parent_session_ui_shell",
    parentTitle: "Subagents UI shell",
    name: "docs-writer",
    role: "documentation",
    status: "idle",
    turnId: "turn_4",
    lastEvent: "11m ago",
    lastOutput: "Drafted acceptance criteria for the read-only inspector shell.",
    model: "openai/gpt-5.4",
    cwd: "/root/docs/pi-agent/ActRail",
    eventCount: 16,
  },
  {
    actorId: "actor_probe_0d55",
    childSessionId: "session_child_probe_0d55",
    parentSessionId: "parent_session_ui_shell",
    parentTitle: "Subagents UI shell",
    name: "runtime-probe",
    role: "diagnostics",
    status: "failed",
    turnId: "turn_2",
    lastEvent: "24m ago",
    lastOutput: "Backend unavailable while fetching actor status. Cached summary remains readable.",
    model: "openai/gpt-5.4-mini",
    cwd: "/root/code/ActRail",
    eventCount: 9,
  },
];

const statusRank: Record<SubagentStatus, number> = {
  waiting_for_parent: 0,
  running: 1,
  failed: 2,
  idle: 3,
  completed: 4,
};

const statusLabel: Record<SubagentStatus, string> = {
  waiting_for_parent: "waiting for parent",
  running: "running",
  failed: "failed",
  idle: "idle",
  completed: "completed",
};

function groupByParent(actors: MockSubagent[]) {
  return actors
    .slice()
    .sort((a, b) => statusRank[a.status] - statusRank[b.status] || a.name.localeCompare(b.name))
    .reduce<Array<{ parentSessionId: string; parentTitle: string; actors: MockSubagent[] }>>((groups, actor) => {
      const last = groups[groups.length - 1];
      if (last?.parentSessionId === actor.parentSessionId) {
        last.actors.push(actor);
      } else {
        groups.push({ parentSessionId: actor.parentSessionId, parentTitle: actor.parentTitle, actors: [actor] });
      }
      return groups;
    }, []);
}

interface SubagentsRailProps {
  selectedActorId: string;
  onSelect(actorId: string): void;
}

export function SubagentsRail({ selectedActorId, onSelect }: SubagentsRailProps) {
  const groups = useMemo(() => groupByParent(mockSubagents), []);
  const waitingCount = mockSubagents.filter((actor) => actor.status === "waiting_for_parent").length;
  const runningCount = mockSubagents.filter((actor) => actor.status === "running").length;

  return (
    <div className="subagentsRailShell">
      <section className="subagentsRailHeader" aria-label="Subagent filters">
        <p className="sessionsEyebrow">Runtime actors</p>
        <h2 className="sessionsSurfaceTitle">Subagents</h2>
        <div className="subagentsRailStats" aria-label="Subagent counts">
          <span>{mockSubagents.length} actors</span>
          <span>{waitingCount} waiting</span>
          <span>{runningCount} running</span>
        </div>
        <input className="subagentsSearch" type="search" placeholder="Search mock actors" aria-label="Search subagents" />
        <div className="subagentsFilterRow" aria-label="Mock filters">
          <Badge variant="outline">status</Badge>
          <Badge variant="outline">role</Badge>
          <Badge variant="outline">parent</Badge>
        </div>
      </section>
      <ScrollArea className="subagentsRailBody">
        {groups.map((group) => (
          <section key={group.parentSessionId} className="subagentsGroup">
            <div className="subagentsGroupHeader">
              <span>{group.parentTitle}</span>
              <small>{group.actors.length}</small>
            </div>
            <div className="subagentsGroupList">
              {group.actors.map((actor) => (
                <button
                  key={actor.actorId}
                  type="button"
                  className={cn("subagentCard", selectedActorId === actor.actorId && "active")}
                  aria-current={selectedActorId === actor.actorId ? "true" : undefined}
                  onClick={() => onSelect(actor.actorId)}
                >
                  <span className="subagentCardTopline">
                    <strong>{actor.name}</strong>
                    <span className={cn("subagentStatusPill", actor.status)}>{statusLabel[actor.status]}</span>
                  </span>
                  <span className="subagentCardMeta">{actor.role}</span>
                  {actor.question ? <span className="subagentCardQuestion">q: {actor.question}</span> : null}
                </button>
              ))}
            </div>
          </section>
        ))}
      </ScrollArea>
    </div>
  );
}

interface SubagentsInspectorProps {
  selectedActorId: string;
}

export function SubagentsInspector({ selectedActorId }: SubagentsInspectorProps) {
  const selected = mockSubagents.find((actor) => actor.actorId === selectedActorId) || mockSubagents[0];

  return (
    <section className="subagentsInspector" aria-label="Subagents read-only inspector">
      <header className="subagentsInspectorHeader">
        <div>
          <p className="sessionsEyebrow">Read-only view</p>
          <h1>Subagents</h1>
        </div>
        <div className="subagentsInspectorSummary" aria-label="Subagent summary">
          <span>{mockSubagents.length} actors</span>
          <span>{mockSubagents.filter((actor) => actor.status === "waiting_for_parent").length} waiting</span>
          <span>{mockSubagents.filter((actor) => actor.status === "failed").length} failed</span>
        </div>
      </header>

      <div className="subagentsInspectorGrid">
        <section className="subagentsPanel subagentsSelectedPanel">
          <div className="subagentsPanelHeader">
            <div>
              <p className="sessionsEyebrow">Selected actor</p>
              <h2>{selected.name}</h2>
            </div>
            <span className={cn("subagentStatusPill", selected.status)}>{statusLabel[selected.status]}</span>
          </div>
          <dl className="subagentsIdentityList">
            <div><dt>role</dt><dd>{selected.role}</dd></div>
            <div><dt>parent</dt><dd>{selected.parentTitle}</dd></div>
            <div><dt>actorId</dt><dd>{selected.actorId}</dd></div>
            <div><dt>childSessionId</dt><dd>{selected.childSessionId}</dd></div>
            <div><dt>model</dt><dd>{selected.model}</dd></div>
            <div><dt>cwd</dt><dd>{selected.cwd}</dd></div>
          </dl>
        </section>

        <section className="subagentsPanel">
          <div className="subagentsPanelHeader">
            <div>
              <p className="sessionsEyebrow">Current turn</p>
              <h2>{selected.turnId}</h2>
            </div>
            <span>{selected.lastEvent}</span>
          </div>
          <p className="subagentsOutputPreview">{selected.lastOutput}</p>
        </section>

        <section className="subagentsPanel subagentsQuestionPanel">
          <div className="subagentsPanelHeader">
            <div>
              <p className="sessionsEyebrow">Pending question</p>
              <h2>{selected.question ? "waiting for parent" : "none"}</h2>
            </div>
          </div>
          {selected.question ? (
            <>
              <p className="subagentsQuestionText">{selected.question}</p>
              <p className="subagentsQuestionContext">{selected.questionContext}</p>
              <p className="subagentsReadOnlyNote">Answers stay in the parent Pi API. This shell intentionally exposes no write controls.</p>
            </>
          ) : (
            <p className="subagentsOutputPreview">No pending parent question.</p>
          )}
        </section>

        <section className="subagentsPanel subagentsTimelinePanel">
          <div className="subagentsPanelHeader">
            <div>
              <p className="sessionsEyebrow">Timeline</p>
              <h2>{selected.eventCount} events</h2>
            </div>
            <Button type="button" variant="outline" disabled>Replay coming later</Button>
          </div>
          <ol className="subagentsTimeline">
            <li><span>subagent.started</span><small>actor created</small></li>
            <li><span>subagent.turn_started</span><small>{selected.turnId}</small></li>
            <li><span>subagent.tool_call</span><small>ask_parent</small></li>
            <li><span>subagent.question</span><small>{selected.lastEvent}</small></li>
          </ol>
        </section>
      </div>
    </section>
  );
}
