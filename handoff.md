# Handoff

## Deployment state

A tmux deployment is running via the repository launcher.

Live endpoints:

- frontend: `http://127.0.0.1:18743`
- backend health: `http://127.0.0.1:8743/healthz`

Operational commands:

- start: `scripts/tmux/start.sh`
- status: `scripts/tmux/status.sh`
- stop: `scripts/tmux/stop.sh`

Current tmux session:

- session: `actrail`
- windows: `backend`, `frontend`

## Docs added

- `deploy.md`
  - deployment flow
  - bind targets
  - overrides
  - verification commands
  - current non-durable registry limitation

## Recent committed work already on main

- `29a30f4` `Launch codex sessions through app-server`
- `96407f7` `app: finish codex runtime transport path`
- `62b55c6` `Wire codex helper input and auth env`
- `3b51590` `Remove obsolete websocket handler e2e test`

These changes were used during manual verification of the Codex session flow.

## Verified manually

Manual verification already succeeded against a live local deployment for the Codex path:

- create new Codex session
- send turn 1
- receive assistant reply `TURN1_OK`
- send turn 2
- receive assistant reply `TURN2_OK`
- inspect frontend page text and browser error state
- delete session successfully

## Unfinished automation scaffold

- `internal/app/codex_conversation_test.go`

This file is committed as a scaffold only. It is currently skipped.

Reason:

- the direct fake-runner Codex fixture used in that test still returns `session runtime input is unavailable`
- this does not match the live deployed path that already passed manual verification
- the scaffold is kept so the next person can finish an automated regression instead of rebuilding the fixture from zero

## Likely next step if automation is resumed

Do not start from browser automation first.

Start by aligning the automated fixture with the live Codex launch path that actually passed:

- use the same `codex app-server` launch shape as production
- mirror the helper-backed path and JSON-RPC bootstrap exactly
- only after that, re-enable the skipped Codex conversation regression

## Working tree intent

The repo should contain three new handoff artifacts from this session:

- `deploy.md`
- `handoff.md`
- `internal/app/codex_conversation_test.go` (skipped scaffold)
