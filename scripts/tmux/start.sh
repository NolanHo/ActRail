#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=./common.sh
source "${SCRIPT_DIR}/common.sh"

require_bin tmux "${TMUX_BIN}"
require_bin go "${GO_BIN}"
require_bin npm "${NPM_BIN}"
require_bin curl "${CURL_BIN}"
require_bin python3 "${PYTHON_BIN}"

ensure_iod_helper_bin
ensure_frontend_build

if session_exists; then
  echo "tmux session already exists: ${SESSION_NAME}" >&2
  echo "run ${SCRIPT_DIR}/stop.sh first or set ACTRAIL_TMUX_SESSION to another name" >&2
  exit 1
fi

backend_url=$(backend_health_url)
frontend_root=$(frontend_url)

if "${CURL_BIN}" -fsS -o /dev/null "${backend_url}" 2>/dev/null; then
  echo "backend port already has a live server: ${backend_url}" >&2
  exit 1
fi
if "${CURL_BIN}" -fsS -o /dev/null "${frontend_root}" 2>/dev/null; then
  echo "frontend port already has a live server: ${frontend_root}" >&2
  exit 1
fi

backend_shell=$(tmux_window_command "$(backend_command)")
frontend_shell=$(tmux_window_command "$(frontend_command)")

"${TMUX_BIN}" new-session -d -s "${SESSION_NAME}" -n backend "${backend_shell}"
"${TMUX_BIN}" set-option -t "${SESSION_NAME}" remain-on-exit on
"${TMUX_BIN}" new-window -t "${SESSION_NAME}" -n frontend "${frontend_shell}"
"${TMUX_BIN}" select-window -t "${SESSION_NAME}:backend"

if ! wait_for_http "${backend_url}"; then
  echo "backend did not become ready: ${backend_url}" >&2
  echo "backend pane tail:" >&2
  show_pane_tail "${SESSION_NAME}:backend" >&2
  exit 1
fi

if ! wait_for_http "${frontend_root}"; then
  echo "frontend did not become ready: ${frontend_root}" >&2
  echo "frontend pane tail:" >&2
  show_pane_tail "${SESSION_NAME}:frontend" >&2
  exit 1
fi

echo "session=${SESSION_NAME}"
echo "backend_window=${SESSION_NAME}:backend"
echo "frontend_window=${SESSION_NAME}:frontend"
echo "backend_url=${backend_url}"
echo "frontend_url=${frontend_root}"
