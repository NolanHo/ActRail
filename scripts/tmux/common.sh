#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
SESSION_NAME=${ACTRAIL_TMUX_SESSION:-actrail}
BACKEND_HOST=${ACTRAIL_BACKEND_HOST:-0.0.0.0}
BACKEND_PORT=${ACTRAIL_BACKEND_PORT:-8743}
FRONTEND_HOST=${ACTRAIL_FRONTEND_HOST:-0.0.0.0}
FRONTEND_PORT=${ACTRAIL_FRONTEND_PORT:-18743}
START_TIMEOUT_SECONDS=${ACTRAIL_START_TIMEOUT_SECONDS:-180}
SUPPORT_BIN_DIR=${ACTRAIL_SUPPORT_BIN_DIR:-${REPO_ROOT}/data/bin}
IOD_HELPER_BIN=${ACTRAIL_IOD_BIN:-${SUPPORT_BIN_DIR}/actrail-iod}
FRONTEND_DIST_DIR=${ACTRAIL_FRONTEND_DIST_DIR:-${REPO_ROOT}/web/dist}

resolve_bin() {
  local env_name=$1
  local fallback=$2
  local value=${!env_name:-}
  if [[ -n "${value}" ]]; then
    printf '%s\n' "${value}"
    return 0
  fi
  if command -v "${fallback}" >/dev/null 2>&1; then
    command -v "${fallback}"
    return 0
  fi
  if [[ "${fallback}" == "go" && -x /usr/local/go/bin/go ]]; then
    printf '%s\n' "/usr/local/go/bin/go"
    return 0
  fi
  printf '%s\n' "${fallback}"
}

GO_BIN=$(resolve_bin ACTRAIL_GO_BIN go)
NPM_BIN=$(resolve_bin ACTRAIL_NPM_BIN npm)
CURL_BIN=$(resolve_bin ACTRAIL_CURL_BIN curl)
TMUX_BIN=$(resolve_bin ACTRAIL_TMUX_BIN tmux)
PYTHON_BIN=$(resolve_bin ACTRAIL_PYTHON_BIN python3)

require_bin() {
  local label=$1
  local value=$2
  if [[ ! -x "${value}" ]] && ! command -v "${value}" >/dev/null 2>&1; then
    echo "missing required binary for ${label}: ${value}" >&2
    exit 1
  fi
}

ensure_iod_helper_bin() {
  mkdir -p "${SUPPORT_BIN_DIR}"
  local build_date
  local git_sha
  build_date=$(date -u +%Y-%m-%d)
  git_sha=$(git rev-parse --short HEAD 2>/dev/null || printf 'unknown')
  "${GO_BIN}" build -ldflags "-X main.buildDate=${build_date} -X main.gitSHA=${git_sha}" -o "${IOD_HELPER_BIN}" ./cmd/actrail-iod
}

ensure_frontend_build() {
  (
    cd "${REPO_ROOT}/web"
    if [[ ! -d node_modules ]]; then
      "${NPM_BIN}" ci
    fi
    "${NPM_BIN}" run build
  )
}

session_exists() {
  "${TMUX_BIN}" has-session -t "${SESSION_NAME}" 2>/dev/null
}

wait_for_http() {
  local url=$1
  local timeout=${2:-${START_TIMEOUT_SECONDS}}
  local elapsed=0
  while (( elapsed < timeout )); do
    if "${CURL_BIN}" -fsS -o /dev/null "${url}"; then
      return 0
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  return 1
}

probe_host() {
  local host=$1
  if [[ -z "${host}" || "${host}" == "0.0.0.0" || "${host}" == "::" ]]; then
    printf '127.0.0.1\n'
    return 0
  fi
  printf '%s\n' "${host}"
}

backend_health_url() {
  printf 'http://%s:%s/healthz\n' "$(probe_host "${BACKEND_HOST}")" "${BACKEND_PORT}"
}

frontend_url() {
  printf 'http://%s:%s\n' "$(probe_host "${FRONTEND_HOST}")" "${FRONTEND_PORT}"
}

backend_command() {
  printf '%s\n' "cd \"${REPO_ROOT}\" && export ACTRAIL_HOST=\"${BACKEND_HOST}\" ACTRAIL_PORT=\"${BACKEND_PORT}\" ACTRAIL_IOD_BIN=\"${IOD_HELPER_BIN}\" && \"${GO_BIN}\" build -o \"${SUPPORT_BIN_DIR}/actrail-server\" ./cmd/actrail-server && exec \"${SUPPORT_BIN_DIR}/actrail-server\""
}

frontend_command() {
  printf '%s\n' "cd \"${REPO_ROOT}\" && exec \"${PYTHON_BIN}\" \"${SCRIPT_DIR}/frontend_static_server.py\" --host \"${FRONTEND_HOST}\" --port \"${FRONTEND_PORT}\" --dir \"${FRONTEND_DIST_DIR}\""
}

tmux_window_command() {
  local command_text=$1
  printf 'env -u NPM_CONFIG_PREFIX bash -lc %q\n' "${command_text}"
}

show_pane_tail() {
  local target=$1
  "${TMUX_BIN}" capture-pane -p -S -40 -t "${target}" 2>/dev/null || true
}
