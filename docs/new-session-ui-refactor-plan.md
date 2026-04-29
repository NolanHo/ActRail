# NewSession UI refactor plan

## Problem

`NewSessionDialog` currently mixes two creation paths with navigation shortcuts:

1. create a fresh runtime
2. resume a backend history thread into a new ActRail slot
3. jump to focused or historical sessions

The third job does not belong here. ActRail already has a permanent SessionsRail for moving between existing sessions. Putting existing-session navigation behind NewSession creates a second, weaker switcher and violates the product model: users should cycle between sessions from the rail, not open a creation modal to switch context.

The component also owns too much state in one file: cwd inspection, resume pagination, Pi history filters, launch defaults, model refresh, focus list, worktree fields, optimistic creation, and form validation. These states have different lifetimes and invalidation rules, but currently share one render tree and one submit path.

## Target model

NewSession has only creation semantics:

- `Start`: create a new runtime from cwd, backend, model, and optional worktree settings.
- `Resume`: create a new ActRail slot from an exact backend history identity.

Existing ActRail slots stay in `SessionsRail`. Focus, live sessions, and imported historical slots are rail concerns, not NewSession concerns.

`NewSessionDialog` becomes an orchestrator for creation/resume only. Each tab owns its own data hook and action contract.

## Component split

```text
NewSessionDialog
  SessionIntentTabs
  StartSessionPanel
    ProjectTargetField
    LaunchSettingsForm
    WorktreeForm
  ResumeSessionPanel
    ResumeSearchControls
    ResumeCandidateList
    ResumeCandidatePager
```

State hooks:

```text
useLaunchDraft
  cwd
  backend
  provider
  model
  reasoning_effort
  fast_mode
  tmux
  worktree

useCwdInspection
  input: cwd, backend
  output: exists, will_create, git_root, git_branch, errors

useResumeCandidates
  input: cwd, backend, page, filters
  output: exact backend history identities, pagination, loading, errors
```

Do not store all panel state in `NewSessionDialog`. Panel unmounting must not erase user input unless the dialog closes.

## API semantics

Use two distinct actions:

- `createSession(draft)`: starts a fresh runtime.
- `resumeSession(candidate_id)`: creates an ActRail slot bound to the selected backend session id and source path.

Do not add `openSession(session_id)` to NewSession. Existing-session selection stays in `SessionsRail`:

```ts
sessionsStoreApi.select(session_id)
```

The Resume panel must display backend identity when available:

```text
ActRail slot if already known: s_5
Pi session: 019dd037-af22-765f-ab5f-7029e1f27d6c
source confidence: exact
cwd: /root/docs
last activity: timestamp
first user: text snippet
```

If a resume candidate already has an ActRail slot, the row should mark it as `already in rail` and avoid creating a duplicate unless the product explicitly supports clone/fork semantics.

## Data rules

- CWD inspection and resume candidates share the same normalized cwd input.
- Resume candidates must come from the disk-scanning endpoint for Pi, not only the ActRail registry.
- Existing ActRail sessions do not appear as navigation targets in NewSession.
- Focus is not shown in NewSession; Focus belongs in the permanent SessionsRail.
- The Start submit button appears only in Start.
- The Resume action button appears only when one candidate is selected.
- Model/provider controls do not appear in Resume unless they affect the resumed runtime launch.

## UI layout

Dialog header:

```text
New session
[Start] [Resume]
```

Start panel:

```text
Project
  cwd input
  cwd status
  session name

Runtime
  backend tabs
  provider
  model
  effort
  fast
  tmux

Branch
  worktree toggle and branch only when backend supports it

Action
  Start session
```

Resume panel:

```text
Search
  cwd input, defaulted from active session
  title filter
  backend selector, default pi

Candidates
  rows with title, cwd, backend id, source confidence, first user, last activity
  pagination controls

Action
  Resume selected
```

NewSession must not contain a Focus list, Recent sessions list, or Open existing tab. Those belong to `SessionsRail`, which stays permanently visible on desktop.

## ASCII surface

```text
Desktop shell
+-------------------------------------------------------------------+
| SessionsRail | Conversation                                      |
|              |                                                   |
| s_5 ForkKV   |  New session                                      |
| s_15 ActRail |  +---------------------------------------------+  |
| s_4 ForkKV   |  | Start                 Resume                 |  |
| ...          |  +---------------------------------------------+  |
|              |  | Start creates a new runtime                  |  |
|              |  |                                             |  |
|              |  | cwd        [/root/docs__________________]    |  |
|              |  | backend    [ Pi ] [ Codex ]                  |  |
|              |  | model      [gpt-5.5_____________________]    |  |
|              |  | effort     [high v]                         |  |
|              |  |                                             |  |
|              |  |                         [Cancel] [Start]    |  |
|              |  +---------------------------------------------+  |
+-------------------------------------------------------------------+
```

```text
Desktop shell
+-------------------------------------------------------------------+
| SessionsRail | Conversation                                      |
|              |                                                   |
| s_5 ForkKV   |  New session                                      |
| s_15 ActRail |  +---------------------------------------------+  |
| s_4 ForkKV   |  | Start                 Resume                 |  |
| ...          |  +---------------------------------------------+  |
|              |  | Resume creates a new ActRail slot from       |  |
|              |  | backend history identity                     |  |
|              |  |                                             |  |
|              |  | cwd        [/root/docs__________________]    |  |
|              |  | title      [ForkKV_____________________]     |  |
|              |  |                                             |  |
|              |  | +---------------------------------------+   |  |
|              |  | | ForkKV                         exact  |   |  |
|              |  | | Pi: 019dd037-af22-765f-ab5f-...      |   |  |
|              |  | | first: 继续 ForkKV 的 tombstone...    |   |  |
|              |  | +---------------------------------------+   |  |
|              |  |                                             |  |
|              |  |              [Newer] [Older] [Resume]       |  |
|              |  +---------------------------------------------+  |
+-------------------------------------------------------------------+
```

Session switching remains outside the modal:

```text
click SessionsRail row -> sessionsStoreApi.select(session_id)
```

## Implementation order

1. Remove Focus/Open-existing UI from `NewSessionDialog`.
2. Extract pure data helpers from `NewSessionDialog.tsx` into `new-session/model.ts`.
3. Extract `useLaunchDraft`, preserving existing default hydration and touched-field behavior.
4. Extract `useCwdInspection` and `useResumeCandidates`; keep the existing endpoint contract unchanged.
5. Split the render tree into `StartSessionPanel` and `ResumeSessionPanel` with current creation/resume behavior preserved.
6. Add explicit Resume action for disk-scanned Pi candidates.
7. Add duplicate guard for candidates already represented by an ActRail slot.
8. Add tests for intent boundaries:
   - Start with no resume id creates a fresh runtime.
   - Resume candidate creates a new bound ActRail slot.
   - Focus rows are not rendered in NewSession.
   - Existing live sessions are not rendered as navigation targets in NewSession.
   - CWD change invalidates resume page and selected candidate.
9. Remove obsolete state from `NewSessionDialog`; the dialog should only track open state and selected intent.

## Non-goals

- Do not add a second session switcher inside NewSession.
- Do not add new backend APIs unless the split exposes an impossible state with existing endpoints.
- Do not put model, effort, or context controls into the composer.
- Do not make Session View thicker; this refactor only changes the creation/resume entry surface.
