# Product positioning notes

ActRail is a human control plane for coding agents.

This is a direction note, not a committed roadmap. Product work should still start from concrete user pain observed in local use.

## Current stance

ActRail should let the human stay in one control surface while agents work across sessions, repositories, terminals, servers, and external systems.

The goal is not to remove human control. The goal is to move human control away from raw workspaces and toward decisions, evidence, interruption, review, and routing.

## What ActRail is not

ActRail should not become a heavy task-management system by default.

A task-first abstraction is too rigid for many coding-agent interactions:

- exploratory work starts as a question or investigation
- a session can change shape while the agent discovers the problem
- some work is a quick command, not a planned task
- forcing every interaction into a task creates workflow tax

ActRail should preserve session flexibility while adding coordination primitives only when local use proves they reduce manual effort.

ActRail also should not become the source of truth for every external system.

- Git remains the source of truth for code history
- GitHub remains the source of truth for PRs and issues
- infra systems remain the source of truth for servers and deployment state
- ActRail aggregates, links, controls, and reviews these surfaces

## Product principle

Do not expose the agent workspace as the primary human interface.

Expose:

- decisions the human must make
- evidence needed for review
- safe actions such as interrupt, approve, retry, answer, switch model, or open artifact
- links between sessions, artifacts, GitHub objects, and runtime state

Keep drill-down access to raw transcript, terminal output, files, and logs. Control requires inspectability.

## Current useful primitives

Session remains the flexible base object.

A session represents an ongoing interaction with a runtime in a working context. It should not require a task object to exist.

ActRail can add lighter coordination objects around sessions:

- run: one execution segment inside a session
- attention item: something requiring human review or decision
- artifact: diff, file, test result, PR, issue comment, log excerpt, or report
- external object: GitHub issue, GitHub PR, CI run, server, deployment, or log source
- agent profile: backend, model, cwd or repo affinity, capabilities, current sessions, and capacity

These objects should emerge from actual workflow needs, not from an abstract project-management model.

## Directional user promise

A useful long-term promise:

ActRail lets one human control local and remote coding agents, answer blockers, review artifacts, and operate linked tools without babysitting terminals or switching between agent workspaces.

Near-term work should validate this promise through concrete features rather than a broad platform rewrite.

## Near-term candidate surfaces

These are candidates because they map to current personal workflow pain:

- context view for a selected session
- scheduler-backed ask_user waits as attention items
- web slash commands as an ActRail command surface
- GitHub issue and PR views inside ActRail
- session-linked artifacts such as diffs, files touched, tests, and logs

The product should stay session-compatible until a stronger abstraction proves itself in daily use.
