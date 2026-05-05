# ActRail deployment

This document describes the built-in tmux deployment flow shipped in this repository. It assumes you run commands from the repository root.

## What this deployment starts

`scripts/tmux/start.sh` does three things:

- builds `cmd/actrail-iod` into `data/bin/actrail-iod`
- builds the frontend into `web/dist`
- starts two long-lived tmux windows:
  - backend: `go run ./cmd/actrail-server`
  - frontend: static file server for `web/dist`

The frontend and backend are separate listeners. This deployment does not add a public reverse proxy. If you need one public entrypoint, put an edge proxy such as Caddy or Nginx in front of both listeners.

## Prerequisites

Required executables:

- `tmux`
- `go`
- `npm`
- `curl`
- `python3`

The tmux scripts auto-build the helper and frontend artifacts. A separate global `actrail-iod` install is not required.

## Default bind targets

Without overrides, the tmux launcher binds:

- backend: `0.0.0.0:8743`
- frontend: `0.0.0.0:18743`

Health check:

- backend: `http://127.0.0.1:8743/healthz`

Frontend root:

- `http://127.0.0.1:18743`

`0.0.0.0` is the bind address. Use `127.0.0.1` for local verification from the same machine.

## Start

```bash
scripts/tmux/start.sh
```

Expected output includes:

- tmux session name
- backend window name
- frontend window name
- backend health URL
- frontend URL

## Status

```bash
scripts/tmux/status.sh
```

This prints:

- tmux windows
- frontend URL
- backend health URL
- HTTP header checks for both listeners
- last lines from backend and frontend panes

## Stop

```bash
scripts/tmux/stop.sh
```

## Common overrides

Set these before `scripts/tmux/start.sh` when defaults are not suitable:

- `ACTRAIL_TMUX_SESSION`
- `ACTRAIL_BACKEND_HOST`
- `ACTRAIL_BACKEND_PORT`
- `ACTRAIL_FRONTEND_HOST`
- `ACTRAIL_FRONTEND_PORT`
- `ACTRAIL_FRONTEND_DIST_DIR`
- `ACTRAIL_GO_BIN`
- `ACTRAIL_NPM_BIN`
- `ACTRAIL_PYTHON_BIN`
- `ACTRAIL_TMUX_BIN`
- `ACTRAIL_START_TIMEOUT_SECONDS`

Example:

```bash
ACTRAIL_TMUX_SESSION=actrail-prod \
ACTRAIL_BACKEND_HOST=0.0.0.0 \
ACTRAIL_BACKEND_PORT=8743 \
ACTRAIL_FRONTEND_HOST=0.0.0.0 \
ACTRAIL_FRONTEND_PORT=18743 \
scripts/tmux/start.sh
```

## Runtime notes

Useful backend environment variables:

- `ACTRAIL_HOST`
- `ACTRAIL_PORT`
- `ACTRAIL_AUTH_PASSWORD`
- `ACTRAIL_AVAILABLE_BACKENDS`
- `ACTRAIL_AVAILABLE_PROVIDERS`
- `ACTRAIL_AVAILABLE_MODELS`
- `ACTRAIL_DEFAULT_BACKEND`
- `ACTRAIL_WS_PATH`
- `ACTRAIL_IOD_BIN`
- `ACTRAIL_DATA_DIR`

ActRail PI sessions use `pi --mode grpc --grpc-socket ...`. Do not add std/rpc fallback to restart paths. The Pi binary must support gRPC in the compiled runtime.

Bun-specific Pi gRPC requirements:

- Load protobuf schema from generated static JSON. Runtime `.proto` loading reaches `protobufjs -> @protobufjs/inquire("fs")`; Bun compiled binaries return `null` for that dynamic require and crash before binding the socket.
- Disable HTTP/2 server push before constructing the gRPC server. gRPC bidirectional streaming does not use HTTP/2 server push. If Bun advertises push, Node gRPC clients reject the connection with `GOAWAY PROTOCOL_ERROR` before any RPC handler runs.
- Wrap protobufjs response serialization with `Buffer.from(...)` under Bun. Bun can return `Uint8Array`; `@grpc/grpc-js` server response framing calls `.copy(...)`.

Current limitation:

- `ACTRAIL_DATA_DIR` creates directories and stores runtime artifacts, but the live session registry is still in-memory. Do not treat this deployment as durable session storage.

## Verification checklist

After start:

```bash
scripts/tmux/status.sh
curl -fsS http://127.0.0.1:8743/healthz
curl -fsSI http://127.0.0.1:18743
```

If backend or frontend does not come up, inspect the pane tails from `scripts/tmux/status.sh` first.
