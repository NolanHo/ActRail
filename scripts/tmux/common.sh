#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
SESSION_NAME=${ACTRAIL_TMUX_SESSION:-actrail}
BACKEND_HOST=${ACTRAIL_BACKEND_HOST:-127.0.0.1}
BACKEND_PORT=${ACTRAIL_BACKEND_PORT:-8743}
FRONTEND_HOST=${ACTRAIL_FRONTEND_HOST:-0.0.0.0}
FRONTEND_PORT=${ACTRAIL_FRONTEND_PORT:-18743}
START_TIMEOUT_SECONDS=${ACTRAIL_START_TIMEOUT_SECONDS:-180}
SUPPORT_BIN_DIR=${ACTRAIL_SUPPORT_BIN_DIR:-${REPO_ROOT}/data/bin}
IOD_HELPER_BIN=${ACTRAIL_IOD_BIN:-${SUPPORT_BIN_DIR}/actrail-iod}
FRONTEND_DIST_DIR=${ACTRAIL_FRONTEND_DIST_DIR:-${REPO_ROOT}/web/dist}
CADDY_CONFIG_FILE=${ACTRAIL_CADDYFILE:-${SCRIPT_DIR}/Caddyfile}

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

resolve_support_bin() {
  local env_name=$1
  local fallback=$2
  local support_candidate=$3
  local value=${!env_name:-}
  if [[ -n "${value}" ]]; then
    printf '%s\n' "${value}"
    return 0
  fi
  if [[ -x "${support_candidate}" ]]; then
    printf '%s\n' "${support_candidate}"
    return 0
  fi
  if command -v "${fallback}" >/dev/null 2>&1; then
    command -v "${fallback}"
    return 0
  fi
  printf '%s\n' "${support_candidate}"
}

GO_BIN=$(resolve_bin ACTRAIL_GO_BIN go)
NPM_BIN=$(resolve_bin ACTRAIL_NPM_BIN npm)
CURL_BIN=$(resolve_bin ACTRAIL_CURL_BIN curl)
TMUX_BIN=$(resolve_bin ACTRAIL_TMUX_BIN tmux)
CADDY_BIN=$(resolve_support_bin ACTRAIL_CADDY_BIN caddy "${SUPPORT_BIN_DIR}/caddy")

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
  "${GO_BIN}" build -o "${IOD_HELPER_BIN}" ./cmd/actrail-iod
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

frontend_site_address() {
  local explicit=${ACTRAIL_FRONTEND_SITE_ADDR:-}
  if [[ -n "${explicit}" ]]; then
    printf '%s\n' "${explicit}"
    return 0
  fi
  if [[ -z "${FRONTEND_HOST}" || "${FRONTEND_HOST}" == "0.0.0.0" ]]; then
    printf ':%s\n' "${FRONTEND_PORT}"
    return 0
  fi
  printf '%s:%s\n' "${FRONTEND_HOST}" "${FRONTEND_PORT}"
}

backend_upstream() {
  local explicit=${ACTRAIL_BACKEND_UPSTREAM:-}
  if [[ -n "${explicit}" ]]; then
    printf '%s\n' "${explicit}"
    return 0
  fi
  printf '%s:%s\n' "${BACKEND_HOST}" "${BACKEND_PORT}"
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

backend_health_url() {
  printf 'http://%s:%s/healthz\n' "${BACKEND_HOST}" "${BACKEND_PORT}"
}

frontend_url() {
  printf 'http://%s:%s\n' "${FRONTEND_HOST}" "${FRONTEND_PORT}"
}

frontend_api_me_url() {
  printf 'http://%s:%s/api/me\n' "${FRONTEND_HOST}" "${FRONTEND_PORT}"
}

backend_command() {
  printf '%s\n' "cd \"${REPO_ROOT}\" && export ACTRAIL_HOST=\"${BACKEND_HOST}\" ACTRAIL_PORT=\"${BACKEND_PORT}\" ACTRAIL_IOD_BIN=\"${IOD_HELPER_BIN}\" && exec \"${GO_BIN}\" run ./cmd/actrail-server"
}

frontend_command() {
  printf '%s\n' "cd \"${REPO_ROOT}\" && export ACTRAIL_FRONTEND_DIST_DIR=\"${FRONTEND_DIST_DIR}\" ACTRAIL_FRONTEND_SITE_ADDR=\"$(frontend_site_address)\" ACTRAIL_BACKEND_UPSTREAM=\"$(backend_upstream)\" && exec \"${CADDY_BIN}\" run --config \"${CADDY_CONFIG_FILE}\" --adapter caddyfile"
}

tmux_window_command() {
  local command_text=$1
  printf 'env -u NPM_CONFIG_PREFIX bash -lc %q\n' "${command_text}"
}

show_pane_tail() {
  local target=$1
  "${TMUX_BIN}" capture-pane -p -S -40 -t "${target}" 2>/dev/null || true
}
