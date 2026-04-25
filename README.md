# ActRail

ActRail is a local control rail for CLI coding-agent sessions. It launches `pi` and `codex` runtimes, tracks live sessions in one place, and exposes a Go API plus a Preact UI for monitoring, prompt dispatch, queue control, and workspace inspection.

## Current scope

ActRail currently provides:

- session creation for `pi` and `codex` backends
- session listing, focus, rename, delete, and metadata edits
- live transcript and session-state streaming over WebSocket
- prompt send, queue replace, interrupt, and UI-response commands over WebSocket
- HTTP bootstrap, auth, health, session snapshots, and other one-shot actions
- read-only workspace browsing and file reads for a session CWD
- Git file-history lookup for files inside the session workspace
- a separate frontend dev server in `web/` and a Go backend in `cmd/actrail-server`

The backend keeps session state in memory. It creates the configured data directory on startup, but durable session persistence is not wired through the runtime logic yet.

## Transport model

Realtime transport uses WebSocket at `/api/ws`.

HTTP is used for bootstrap and snapshot-style reads such as `/api/bootstrap`, `/api/sessions`, `/api/sessions/{id}/messages`, workspace reads, Git file-version lookups, auth, and one-shot mutations such as rename, focus, delete, restart, and handoff requests.

In practice: use HTTP to discover state, then keep the UI current through WebSocket subscriptions and realtime commands.

## Deferred or partial features

These areas are present as config flags, UI placeholders, or stubbed API shapes, but they are not fully implemented end to end yet:

- voice
- harness
- notifications
- workspace writes from the ActRail API
- session resume from an existing session id
- runtime restart
- session handoff
- durable storage backed by the computed SQLite path
- backend-served production frontend assets

## Repository layout

- `cmd/actrail-server/` - Go entrypoint
- `internal/` - application, adapters, HTTP API, WebSocket transport, and domain logic
- `web/` - Preact frontend
- `scripts/tmux/` - local tmux launcher scripts for backend and frontend

## Backend development

Run the Go tests:

```bash
make test
```

Run the backend with the default config:

```bash
make run
```

Equivalent direct command:

```bash
go run ./cmd/actrail-server
```

By default the backend listens on `127.0.0.1:8080`.

## Frontend development

Install frontend dependencies:

```bash
cd web
npm ci
```

Start the Vite dev server:

```bash
cd web
CODEX_WEB_PORT=8080 npm run dev
```

`CODEX_WEB_PORT` tells the frontend proxy which backend port to use. The frontend config defaults it to `8743`, which matches the tmux launcher. If you run the Go server directly with its default `ACTRAIL_PORT=8080`, set `CODEX_WEB_PORT=8080` as shown above.

Run frontend tests:

```bash
cd web
npm test
```

## tmux launcher

The tmux scripts start both processes and wait for backend health, frontend root, and frontend API proxy readiness.

Start:

```bash
scripts/tmux/start.sh
```

Status:

```bash
scripts/tmux/status.sh
```

Stop:

```bash
scripts/tmux/stop.sh
```

Default tmux launcher ports:

- backend: `127.0.0.1:8743`
- frontend: `127.0.0.1:31250`

Useful tmux launcher overrides live in `scripts/tmux/common.sh`, including:

- `ACTRAIL_TMUX_SESSION`
- `ACTRAIL_BACKEND_HOST`
- `ACTRAIL_BACKEND_PORT`
- `ACTRAIL_FRONTEND_HOST`
- `ACTRAIL_FRONTEND_PORT`
- `ACTRAIL_START_TIMEOUT_SECONDS`

## Environment variables

Core runtime settings:

| Variable | Default | Effect |
| --- | --- | --- |
| `ACTRAIL_PORT` | `8080` | Backend listen port. |
| `ACTRAIL_AUTH_PASSWORD` | unset | When set, enables password auth and requires login to access protected HTTP and WebSocket routes. When unset, ActRail runs in local no-password mode. |
| `ACTRAIL_DATA_DIR` | `./data` | Data directory created on startup. Relative paths are resolved from the backend process working directory, so the default becomes `./data` under wherever you start the server. |

Other useful backend settings:

| Variable | Default | Effect |
| --- | --- | --- |
| `ACTRAIL_HOST` | `127.0.0.1` | Backend listen host. |
| `ACTRAIL_AVAILABLE_BACKENDS` | `pi,codex` | Backend choices exposed to the launch UI. |
| `ACTRAIL_AVAILABLE_PROVIDERS` | unset | Optional provider list exposed in launch defaults. |
| `ACTRAIL_AVAILABLE_MODELS` | unset | Optional model list exposed in launch defaults. |
| `ACTRAIL_DEFAULT_BACKEND` | `pi` | Default backend in launch defaults. |
| `ACTRAIL_WS_PATH` | `/api/ws` | WebSocket endpoint advertised by bootstrap. |

## Notes on persistence

`ACTRAIL_DATA_DIR` currently guarantees directory creation only. The config also computes a SQLite path at `<data-dir>/actrail.db`, but the live app still uses the in-memory session registry. Do not treat the current runtime as durable session storage.
