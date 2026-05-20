# ActRail deployment

ActRail uses supervisord on this host. The backend and the Caddy frontend must run as supervisord programs so process state, restart policy, and logs have one owner.

Repository root:

```text
/root/code/ActRail
```

Runtime state:

```text
/root/code/ActRail/data
```

Public local endpoints:

```text
backend health: http://127.0.0.1:8743/healthz
frontend and API entrypoint: http://127.0.0.1:18743
```

## Required commands

```text
supervisorctl: /nix/var/ml-platform-env/bin/supervisorctl
go: /usr/local/go/bin/go
npm: /vePFS-Mindverse/user/nolanho/code/pi/fork/pi-stack-2026-03-27/runtime/bin/npm
```

Set helper variables before supervisor operations:

```bash
SUP=/nix/var/ml-platform-env/bin/supervisorctl
CONF=/mlplatform/supervisord/supervisord.conf
```

## Build artifacts

```bash
cd /root/code/ActRail
/usr/local/go/bin/go build -o data/bin/actrail-server ./cmd/actrail-server
/usr/local/go/bin/go build -ldflags "-X main.buildDate=$(date -u +%Y-%m-%d) -X main.gitSHA=$(git rev-parse --short HEAD)" -o data/bin/actrail-iod ./cmd/actrail-iod
cd web
/vePFS-Mindverse/user/nolanho/code/pi/fork/pi-stack-2026-03-27/runtime/bin/npm ci
/vePFS-Mindverse/user/nolanho/code/pi/fork/pi-stack-2026-03-27/runtime/bin/npm run build
```

## Caddy

Caddy owns `:18743`. It serves `web/dist` and reverse-proxies `/api*` to the backend on `127.0.0.1:8743`.

`/root/code/ActRail/data/caddy/Caddyfile`:

```caddyfile
{
	admin off
	auto_https off
}

:18743 {
	encode zstd gzip

	handle /api* {
		reverse_proxy 127.0.0.1:8743
	}

	handle {
		root * /root/code/ActRail/web/dist
		try_files {path} /index.html
		file_server
	}
}
```

Validate config:

```bash
/root/code/ActRail/data/bin/caddy validate --config /root/code/ActRail/data/caddy/Caddyfile --adapter caddyfile
```

## Supervisord program config

`/mlplatform/supervisord/conf.d/actrail.conf`:

```ini
[program:actrail-backend]
command=/root/code/ActRail/data/bin/actrail-server
process_name=%(program_name)s
numprocs=1
directory=/root/code/ActRail
priority=40
autostart=true
autorestart=true
startsecs=2
startretries=5
stopsignal=TERM
stopasgroup=true
killasgroup=true
user=root
stdout_logfile=/var/lib/actrail/actrail-backend.supervisor.stdout.log
stdout_logfile_maxbytes=10MB
stdout_logfile_backups=3
stderr_logfile=/var/lib/actrail/actrail-backend.supervisor.stderr.log
stderr_logfile_maxbytes=10MB
stderr_logfile_backups=3
environment=ACTRAIL_HOST="127.0.0.1",ACTRAIL_PORT="8743",ACTRAIL_DATA_DIR="/root/code/ActRail/data",ACTRAIL_IOD_BIN="/root/code/ActRail/data/bin/actrail-iod",ACTRAIL_AUTH_USERNAME="nolan",ACTRAIL_AUTH_PASSWORD="<password>",ACTRAIL_OTEL_ENDPOINT="192.168.4.70:14317",ACTRAIL_OTEL_PROTOCOL="grpc",ACTRAIL_OTEL_INSECURE="true",PATH="/root/.pi/agent/bin:/usr/local/go/bin:/root/.pi/agent-stable/bin:/root/.npm-global/bin:/vePFS-Mindverse/user/nolanho/code/pi-agent/node_modules/.bin:/vePFS-Mindverse/user/nolanho/code/pi/fork/pi-stack-2026-03-27/runtime/bin:/vePFS-Mindverse/user/nolanho/cache/go/bin:/root/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

[program:actrail-frontend]
command=/root/code/ActRail/data/bin/caddy run --config /root/code/ActRail/data/caddy/Caddyfile --adapter caddyfile
process_name=%(program_name)s
numprocs=1
directory=/root/code/ActRail
priority=41
autostart=true
autorestart=true
startsecs=2
startretries=5
stopsignal=TERM
stopasgroup=true
killasgroup=true
user=root
stdout_logfile=/var/lib/actrail/actrail-frontend.supervisor.stdout.log
stdout_logfile_maxbytes=10MB
stdout_logfile_backups=3
stderr_logfile=/var/lib/actrail/actrail-frontend.supervisor.stderr.log
stderr_logfile_maxbytes=10MB
stderr_logfile_backups=3
environment=HOME="/root",XDG_CONFIG_HOME="/root/.config",XDG_DATA_HOME="/root/.local/share",PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
```

`autorestart=true` is required for both programs. If `actrail-backend` exits after `startsecs`, supervisord starts it again. `stopasgroup=true` and `killasgroup=true` prevent orphaned subprocesses during restart.

Apply config:

```bash
$SUP -c "$CONF" reread
$SUP -c "$CONF" update
$SUP -c "$CONF" status actrail-backend actrail-frontend
```

## Restart after code changes

```bash
cd /root/code/ActRail
/usr/local/go/bin/go build -o data/bin/actrail-server ./cmd/actrail-server
/usr/local/go/bin/go build -ldflags "-X main.buildDate=$(date -u +%Y-%m-%d) -X main.gitSHA=$(git rev-parse --short HEAD)" -o data/bin/actrail-iod ./cmd/actrail-iod
cd web
/vePFS-Mindverse/user/nolanho/code/pi/fork/pi-stack-2026-03-27/runtime/bin/npm ci
/vePFS-Mindverse/user/nolanho/code/pi/fork/pi-stack-2026-03-27/runtime/bin/npm run build
$SUP -c "$CONF" restart actrail-backend actrail-frontend
```

## Verify deployment

```bash
$SUP -c "$CONF" status actrail-backend actrail-frontend
ss -ltnp '( sport = :8743 or sport = :18743 )'
curl -fsS http://127.0.0.1:8743/healthz
curl -fsSI http://127.0.0.1:18743/
curl -fsS http://127.0.0.1:18743/api/me
```

Expected state:

```text
actrail-backend RUNNING
actrail-frontend RUNNING
:8743 owned by actrail-server
:18743 owned by caddy
backend health returns 200
frontend root returns 200
frontend /api/me through Caddy returns 200
```

## Runtime notes

Backend environment variables:

- `ACTRAIL_HOST`
- `ACTRAIL_PORT`
- `ACTRAIL_DATA_DIR`
- `ACTRAIL_IOD_BIN`
- `ACTRAIL_AUTH_USERNAME`
- `ACTRAIL_AUTH_PASSWORD`
- `ACTRAIL_OTEL_ENDPOINT` (OTLP gRPC collector endpoint, host:port)
- `ACTRAIL_OTEL_PROTOCOL`
- `ACTRAIL_OTEL_INSECURE`

ActRail Pi sessions use `pi --mode grpc --grpc-socket ...`. Restart paths must not add std/rpc fallback. The Pi binary must support gRPC in the compiled runtime.

`ACTRAIL_DATA_DIR=/root/code/ActRail/data` keeps SQLite, IOD manifests, helper bindings, and Caddy config in the existing state directory. Changing it creates a separate runtime state.

## Logs

```bash
tail -f /var/lib/actrail/actrail-backend.supervisor.stdout.log
tail -f /var/lib/actrail/actrail-backend.supervisor.stderr.log
tail -f /var/lib/actrail/actrail-frontend.supervisor.stdout.log
tail -f /var/lib/actrail/actrail-frontend.supervisor.stderr.log
tail -f /mlplatform/supervisord/supervisord.log
```

Runtime state lives under:

```text
/root/code/ActRail/data/sqlite/actrail.db
/root/code/ActRail/data/runtime/
```

## Troubleshooting

Backend cannot bind `:8743`:

```bash
ss -ltnp '( sport = :8743 )'
```

Stop the competing process, then restart the supervised backend:

```bash
$SUP -c "$CONF" restart actrail-backend
```

Supervisor does not pick up config:

```bash
ls -l /mlplatform/supervisord/conf.d/actrail.conf
$SUP -c "$CONF" reread
$SUP -c "$CONF" update
```

Only files ending in `.conf` are included by the supervisord include glob.
