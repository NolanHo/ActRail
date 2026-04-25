#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=./common.sh
source "${SCRIPT_DIR}/common.sh"

require_bin tmux "${TMUX_BIN}"

if ! session_exists; then
  echo "tmux session not running: ${SESSION_NAME}"
  exit 0
fi

"${TMUX_BIN}" kill-session -t "${SESSION_NAME}"
echo "stopped tmux session: ${SESSION_NAME}"
