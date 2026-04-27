#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=./common.sh
source "${SCRIPT_DIR}/common.sh"

require_bin tmux "${TMUX_BIN}"
require_bin curl "${CURL_BIN}"

if ! session_exists; then
  echo "tmux session not running: ${SESSION_NAME}"
  exit 1
fi

echo "session=${SESSION_NAME}"
"${TMUX_BIN}" list-windows -t "${SESSION_NAME}" -F 'window=#{window_name} pane=#{pane_id} active=#{window_active}'
echo "frontend_url=$(frontend_url)"
echo "backend_url=$(backend_health_url)"

echo "frontend_check:"
"${CURL_BIN}" -fsSI "$(frontend_url)" || true

echo "backend_check:"
"${CURL_BIN}" -fsSI "$(backend_health_url)" || true

echo "backend_pane_tail:"
show_pane_tail "${SESSION_NAME}:backend"

echo "frontend_pane_tail:"
show_pane_tail "${SESSION_NAME}:frontend"
