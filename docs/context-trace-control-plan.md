# Context trace and agent control view plan

This is a planning note. Do not treat it as an implementation commitment until the agent/team interaction model exists in the runtime layer.

## Motivation

ActRail currently shows a selected session primarily as a conversation stream. That view is useful for reading messages, but it is not enough for future agent/team coordination.

The intended future problem is not only "show context differently". The intended problem is control:

- understand how an agent reached a state
- inspect which tool calls, files, outputs, and decisions led to the current answer
- control interactions between a parent agent and teams
- route human attention when one participant blocks another
- review execution structure without reading a raw transcript

A trace view is useful because it can become the visual substrate for agent/team control. It should remain planned until the runtime can expose the necessary interaction events.

## Current limitation

A tree view over the current transcript can be built, especially for Pi sessions where events already carry `event_id`, `parent_event_id`, and `tool_call_id`.

That would only be an execution trace approximation. It would not yet represent real agent/team interaction because ActRail does not currently have runtime-level team semantics.

The missing prerequisite lives below ActRail:

- pi-agent needs explicit parent-agent/team interaction support
- the runtime needs to emit structured events for delegation, team lifecycle, team messages, tool calls, returns, cancellation, and blockers
- ActRail should consume those events rather than inventing a fake tree from transcript order

## Planned view

The planned view should be a second view for a selected session, alongside the conversation stream.

Working names:

- Trace
- Context Chain
- Control Trace

Avoid naming it `Context` as if it were the true model context window. ActRail can show observable execution structure; it cannot claim to show the full hidden LLM context unless the runtime provides it.

## Target shape

A future trace/control tree could look like this:

```text
Session
  Parent agent
    User request
    Plan / reasoning boundary
    Tool call group
      read
      edit
      bash
    Delegate to team: review frontend composer
      Team session/run
        tool calls
        findings
        blocker
        return summary
    Delegate to team: inspect backend API
      Team session/run
        tool calls
        findings
        return summary
    Parent synthesis
    Final answer / artifact
```

The important object is not the visual tree itself. The important object is the typed relationship between participants and events.

## Event relationships needed

The runtime should eventually expose relationships such as:

```ts
type TraceEdgeKind =
  | "parent_child"
  | "delegates_to"
  | "returns_to"
  | "tool_call_result"
  | "blocks"
  | "answers"
  | "cancels"
  | "references_artifact"
```

ActRail should distinguish explicit runtime edges from inferred UI edges:

```ts
type EdgeConfidence = "explicit" | "inferred";
```

Inferred edges are acceptable for display aids. They must not drive control behavior.

## Tool call compression

Tool calls must be compressed by default.

A collapsed node should show:

- tool name
- status: running, pass, fail, cancelled
- short argument summary
- short result summary
- linked files or command when present
- duration when available

Raw arguments and output belong in an expandable detail drawer.

This keeps the trace useful as a control surface instead of another noisy log stream.

## Control actions this view may later support

When runtime support exists, the view can expose actions at the right node:

- interrupt parent agent
- interrupt team
- cancel delegation
- answer blocker
- promote team finding into parent context
- open artifact
- compare team outputs
- retry a failed branch
- detach a team into a normal session

These actions require runtime contracts. Do not implement them as frontend-only state.

## Near-term plan

Keep this as a plan until pi-agent supports explicit team interaction.

A lightweight pre-runtime prototype is still possible later:

- add Conversation / Trace tabs for Pi sessions
- build a tree from `event_id`, `parent_event_id`, `tool_call_id`, and sequence order
- mark all non-runtime links as inferred
- collapse tool calls by default
- expose raw event details

That prototype should not be sold as team control. It is only a trace viewer.

## Non-goals for now

- no implementation before runtime-level team support
- no fake team model built only from transcript grouping
- no claim that the view shows full LLM context
- no cross-session coordination UI until relationships are explicit
- no control action unless the runtime can honor it
