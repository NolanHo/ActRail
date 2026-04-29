# Scheduler wait UI plan

This document defines the UI shape for GitHub issue #1, `ask_user` as a scheduler-backed wait primitive.

The UI must not replace the existing transient ask-user bridge until the backend wait contract exists. The current `AskUserCard` path remains the compatibility layer for legacy `ui.request` and ask-user-like transcript events.

## Product model

A wait is a durable scheduler object owned by ActRail server state. It is not just a message card and not just a websocket request.

The UI has three projections over the same wait state:

```text
Wait state in SQLite and websocket snapshots
  Conversation anchor
  Details side panel
  Waiting inbox
```

Conversation remains the primary surface. Wait handling appears beside it, not instead of it.

## Desktop shape

```text
+--------------------------------------------------------------------------------+
| ActRail                           Runtime: waiting      Waits: 2     Settings   |
+----------------------+---------------------------------------------------------+
| Sessions             | Conversation                         | Details           |
|                      |                                      |                   |
| s_5 ForkKV           | User                                 | Tabs              |
|   running            |   Continue                           | [Files] [Wait]    |
|                      |                                      |                   |
| s_9 ActRail          | Assistant                            | WAIT              |
|   waiting on user    |   I need a decision before...        |                   |
|                      |                                      | Question          |
|                      | +----------------------------------+ | Which migration   |
|                      | | WAITING ON USER                  | | path should I use?|
|                      | | Which migration path should I use?| |                   |
|                      | | Blocking reason: schema choice    | | Blocking reason  |
|                      | | [Open wait] [Claim] [Cancel]      | | Schema choice...  |
|                      | +----------------------------------+ |                   |
|                      |                                      | Attempted         |
|                      | Tool trace collapsed                 | Checked existing   |
|                      |                                      | migrations...      |
|                      |                                      |                   |
|                      |                                      | Default if no reply|
|                      |                                      | Use path A         |
|                      |                                      |                   |
|                      |                                      | [Claim] [Cancel]   |
|                      |                                      |                   |
|                      |                                      | Answer             |
|                      |                                      | disabled until     |
|                      |                                      | claimed            |
+----------------------+--------------------------------------+-------------------+
| Composer disabled: session is waiting on user      [Open wait] [Cancel wait]   |
+--------------------------------------------------------------------------------+
```

The right column is the existing `SessionWorkspace` surface, but the user-facing label should be `Details`, not `Workspace`. Code can keep the current component name until a broader rename is justified.

## Waiting inbox

The inbox is a cross-session entry point. It lists active waits and lets the operator jump to the owning session.

```text
+--------------------------------------------------+
| Waiting Inbox                                    |
+--------------------------------------------------+
| Active waits                                     |
|                                                  |
| +----------------------------------------------+ |
| | ForkKV                                       | |
| | Which migration path should I use?           | |
| | state: pending_unread   age: 4m   timeout:11m| |
| | [Open] [Claim]                               | |
| +----------------------------------------------+ |
|                                                  |
| +----------------------------------------------+ |
| | ActRail                                      | |
| | Should stale runtime waits become orphaned?  | |
| | state: claimed          claimed by: you      | |
| | [Open]                                      | |
| +----------------------------------------------+ |
+--------------------------------------------------+
```

`Open` behavior:

- select the owning session
- open the `Wait` tab in Details
- select the wait thread
- optional later improvement: scroll conversation to the wait anchor

## Wait details tab

Pending or unclaimed state:

```text
+-------------------+
| Details           |
| [Files] [Wait]    |
+-------------------+
| WAIT              |
| state: unread     |
|                   |
| Question          |
| Which migration   |
| path should I use?|
|                   |
| Context           |
| Long context...   |
|                   |
| Blocking reason   |
| Schema choice...  |
|                   |
| Attempted         |
| Checked existing  |
| migrations...     |
|                   |
| Default if no reply|
| Use path A        |
|                   |
| Files             |
| internal/app/...  |
|                   |
| [Claim] [Cancel]  |
+-------------------+
```

Claimed state:

```text
+-------------------+
| WAIT              |
| state: claimed    |
|                   |
| Question          |
| Which migration...|
|                   |
| Justification     |
| Blocking reason   |
| Attempted         |
| Default fallback  |
|                   |
| Answer            |
| +---------------+ |
| | type answer   | |
| |               | |
| +---------------+ |
| [Submit answer]   |
| [Cancel wait]     |
+-------------------+
```

Terminal state:

```text
+-------------------+
| WAIT              |
| state: timed out  |
|                   |
| Question          |
| Which migration...|
|                   |
| Fallback used     |
| Use path A        |
|                   |
| This wait is read-|
| only.             |
+-------------------+
```

## Mobile shape

Conversation remains visible. Opening a wait uses a full-height sheet.

```text
+--------------------------------+
| ActRail       Runtime: waiting |
+--------------------------------+
| Conversation                   |
|                                |
| User: Continue                 |
|                                |
| +----------------------------+ |
| | WAITING ON USER            | |
| | Which migration path...?   | |
| | [Open wait]                | |
| +----------------------------+ |
|                                |
+--------------------------------+
| Composer disabled             |
| [Open wait] [Cancel wait]     |
+--------------------------------+
```

```text
+--------------------------------+
| Wait                      Close|
+--------------------------------+
| Question                       |
| Which migration path...?       |
|                                |
| Blocking reason                |
| Schema choice...               |
|                                |
| Attempted                      |
| Checked existing migrations... |
|                                |
| Default if no reply            |
| Use path A                     |
|                                |
| [Claim] [Cancel]               |
+--------------------------------+
```

## Compatibility model

There are two UI paths during migration.

```text
legacy ui.request or ask-user-like transcript event
  AskUserCard
  SessionUiStore
  api.submitUiResponse

new durable wait event or active_wait snapshot
  WaitCard
  WaitsStore
  wait lifecycle HTTP endpoints
```

Do not merge `Wait` into `SessionUiRequest`. They have different ownership and lifecycle rules.

`SessionUiRequest` characteristics:

- transient runtime UI request
- source of truth is the runtime or current websocket state
- answer path is `RespondUI`
- existing history and live sessions depend on it

`Wait` characteristics:

- durable scheduler object
- source of truth is SQLite
- server owns claim, timeout, cancel, orphan, answer
- answer path returns structured wait result to the runtime adapter

## Render routing

Conversation event routing:

```text
if event has wait_id or event.type is wait:
  render WaitCard
else if event is ask-user-like:
  render AskUserCard
else:
  render existing message/tool/error renderer
```

Active session blocking:

```text
if session.active_wait exists:
  disable normal Composer
  show disabled reason with Open wait and Cancel wait actions
else:
  keep current Composer behavior
```

Do not disable the normal composer for legacy `ui.request` unless a separate product decision requires that behavior.

## State behavior

```text
pending_unread
  Conversation: WAITING ON USER
  Inbox: active
  Details: Claim visible, answer hidden
  Composer: disabled

claimed
  Conversation: CLAIMED
  Inbox: active
  Details: answer form visible
  Composer: disabled

answered
  Conversation: ANSWERED
  Inbox: closed or absent from active list
  Details: read-only answer
  Composer: enabled after backend clears active wait

timed_out_locked
  Conversation: TIMED OUT
  Inbox: closed or absent from active list
  Details: read-only fallback_used
  Composer: enabled after backend clears active wait

cancelled
  Conversation: CANCELLED
  Details: read-only terminal state
  Composer: enabled after backend clears active wait

orphaned
  Conversation: ORPHANED
  Details: read-only terminal state
  Composer: enabled after backend clears active wait
```

## Frontend module split

```text
web/src/domains/waits/
  types.ts
  api.ts
  store.ts
  normalize.ts

web/src/components/waits/
  WaitCard.tsx
  WaitInbox.tsx
  WaitThreadPanel.tsx
  WaitStateBadge.tsx
  WaitJustification.tsx
  WaitAnswerForm.tsx

web/src/components/conversation/ConversationPane.tsx
  route wait events to WaitCard
  keep AskUserCard for legacy events

web/src/components/composer/Composer.tsx
  accept active wait disabled state or derive it from waits store

web/src/components/workspace/SessionWorkspace.tsx
  add Details tabs: Files, Wait, Diagnostics as needed
```

## Backend snapshot dependencies

The UI depends on these backend surfaces from issue #1.

Session state snapshot should include active wait summary:

```ts
type ActiveWaitSummary = {
  wait_id: string;
  thread_id: string;
  state: "pending_unread" | "claimed";
  question: string;
  blocking_reason?: string;
  timeout_at?: number;
  claimed_at?: number;
};
```

Websocket events update `WaitsStore`:

```text
wait.created
wait.claimed
wait.answered
wait.timed_out
wait.cancelled
wait.orphaned
waits.updated
```

HTTP endpoints used by UI:

```text
GET  /api/waits/inbox
GET  /api/sessions/{session_id}/waits/threads
GET  /api/sessions/{session_id}/waits/threads/{thread_id}
POST /api/sessions/{session_id}/waits/{wait_id}/claim
POST /api/sessions/{session_id}/waits/{wait_id}/answer
POST /api/sessions/{session_id}/waits/{wait_id}/cancel
```

## Implementation order

1. Add wait frontend types and pure state helpers.
2. Add `WaitCard` with mocked props and tests.
3. Add `WaitThreadPanel` with mocked props and tests.
4. Add `WaitInbox` with mocked props and tests.
5. Add `WaitsStore` and HTTP client methods once backend endpoints exist.
6. Wire websocket wait events into `WaitsStore`.
7. Add `active_wait` to session state types and composer disabling.
8. Add Details `Wait` tab to `SessionWorkspace`.
9. Route durable wait events in `ConversationPane` while preserving `AskUserCard` routing.
10. Delete no legacy ask-user code until backend migration proves all old paths are unused.

## Non-goals for the UI pass

- no replacement of existing `AskUserCard`
- no plain-text timeout/cancel/orphan answer through current composer
- no normal composer replies to active waits
- no main `Conversation` versus `Waits` tab that hides conversation
- no multi-wait session UI in v0
- no frontend-only wait lifecycle state that can disagree with server state
