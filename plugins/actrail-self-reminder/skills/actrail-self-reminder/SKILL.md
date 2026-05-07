---
name: actrail-self-reminder
description: Schedule, list, or cancel ActRail Scheduler self-reminders for Codex sessions. Use when the user asks Codex to remind itself later, trigger a future note, inspect pending self-reminders, or cancel a self-reminder. Do not use for Supervisor runs.
---

# ActRail Self Reminder

Use this skill when the user wants the agent to schedule a future note back into an ActRail session.

Self-reminder is a Scheduler event type, and it is not Supervisor. A created self-reminder becomes a `scheduler_items` row with `kind: "self_reminder"`; when due, Scheduler stages it into Inbox with `source: "self_reminder"` and delivery waits for the session idle gate.

## Command

Use the bundled CLI from the ActRail repository:

```bash
actrail-self-reminder --help
```

Environment:

- `ACTRAIL_BASE_URL`: ActRail API base URL. Defaults to `http://127.0.0.1:18743`.
- `ACTRAIL_SESSION_ID`: default target session id for `create`.
- `ACTRAIL_AUTH_PASSWORD`: optional password used to call `/api/login` before API requests.
- `ACTRAIL_AUTH_COOKIE_HEADER`: optional raw `Cookie` header for authenticated requests.
- `ACTRAIL_AUTH_TOKEN`: optional auth token; uses `ACTRAIL_AUTH_COOKIE_NAME`, `ACTRAIL_AUTH_COOKIE`, or `actrail_auth` as the cookie name.

## Workflow

1. Identify the target session. Prefer `ACTRAIL_SESSION_ID` or an explicit user-provided session id. Use `sessions` when discovery is needed.
2. Create a self-reminder only when the user has asked for a future self-message or reminder.
3. Use conservative titles and clear messages. If no title is specified, let ActRail default it to `Self Reminder`.
4. Report the created `item_id`, target `session_id`, due time, and current state.

Examples:

```bash
actrail-self-reminder sessions
actrail-self-reminder list --session-id "$ACTRAIL_SESSION_ID"
actrail-self-reminder create --session-id "$ACTRAIL_SESSION_ID" --after 10m --message "Check whether the build finished."
actrail-self-reminder cancel self_reminder_123
```

Do not expose Supervisor through this skill. Supervisor is an ActRail-native user simulation capability and should remain invisible to the agent being supervised.
