# Scheduler and Inbox Plan

ActRail routes all asynchronous agent input through one backend-owned pipeline:

```text
Producer -> scheduler_items -> inbox_items -> dispatcher -> user message
```

SQLite persists scheduler state, inbox state, settings, and delivery history. The frontend reads state and requests user actions such as cancel or settings update. The backend owns all state transitions.

## Product model

Global Scheduler is a top-level view in the left global view rail, at the same level as Sessions and Subagents. It is not part of Workspace and not part of Settings.

Workspace Inbox is a per-session pane opened from the active session toolbar. It shows pending, delivered, cancelled, and failed inbox items for the active session.

Supervisor is not a workspace primary tool. It becomes a Scheduler preset and an Inbox source. Existing per-session policy remains, but supervisor output emits an inbox item instead of directly injecting a prompt.

## Backend objects

`scheduler_settings` stores global dispatcher settings. Initial setting:

```text
idle_before_delivery_seconds = 30
```

`scheduler_items` stores producer schedules such as alarms and supervisor preset runs.

`scheduler_items.kind` values:

```text
alarm
supervisor
```

`inbox_items` stores session-scoped deliverable messages.

`inbox_items.source` values:

```text
alarm
supervisor
manual
```

`inbox_items.state` values:

```text
pending
claimed
delivered
cancelled
failed
```

## Message envelope

Dispatcher sends one user message per delivered inbox item:

```xml
<Inbox>
<title>Alarm Response</title>
<source>alarm</source>
<message>...</message>
</Inbox>
```

Supervisor uses:

```xml
<Inbox>
<title>Supervisor Suggestion</title>
<source>supervisor</source>
<message>...</message>
</Inbox>
```

## Dispatcher rules

An inbox item is deliverable only when:

```text
state = pending
due_at <= now
session is not busy
session queue is empty
session has no unresolved UI request
session has no active wait
transport has no control error
session idle duration >= idle_before_delivery_seconds
```

Delivery must atomically claim the inbox item, recheck session state, send the envelope as a user message, then mark the item delivered with the message id. Failed sends mark the item failed with an error.

## Implementation sequence

1. SQLite + app model foundation:
   - migrations for scheduler settings, scheduler items, inbox items
   - app APIs for global scheduler snapshot and session inbox snapshot
   - no dispatcher yet

2. UI shell foundation:
   - add Scheduler top-level global view
   - add Workspace Inbox toolbar button and pane
   - show read-only persisted backend snapshots

3. Alarm producer:
   - server API for creating alarms
   - server-side entry point equivalent to SetAlarm(duration, message)
   - due alarms emit inbox items

4. Dispatcher:
   - idle timer and busy/queue/wait/transport gate
   - atomic claim and delivered/failed state transitions

5. Supervisor migration:
   - supervisor run with inject decision creates inbox item
   - remove direct supervisor prompt injection path
   - expose supervisor as Scheduler preset and Inbox source filter
