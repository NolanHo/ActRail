#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE_DIR="${1:-/root/.local/share/codoxear}"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/actrail-codoxear-import-XXXXXX")"
SNAPSHOT_DIR="$WORK_DIR/source"
TARGET_DIR="$WORK_DIR/actrail-data"
REPORT_PATH="$WORK_DIR/import-report.json"
SOURCE_DB="$SOURCE_DIR/sqlite.db"
TARGET_DB="$TARGET_DIR/actrail.db"

mkdir -p "$SNAPSHOT_DIR" "$TARGET_DIR"

python3 - "$SOURCE_DB" "$SNAPSHOT_DIR/sqlite.db" <<'PY'
import sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
source = sqlite3.connect(src)
target = sqlite3.connect(dst)
with target:
    source.backup(target)
source.close()
target.close()
PY

for name in \
  session_aliases.json \
  session_sidebar.json \
  hidden_sessions.json \
  session_files.json \
  session_queues.json \
  recent_cwds.json \
  cwd_groups.json \
  voice_settings.json
 do
  if [[ -f "$SOURCE_DIR/$name" ]]; then
    cp -p "$SOURCE_DIR/$name" "$SNAPSHOT_DIR/$name"
  fi
 done

/usr/local/go/bin/go run "$ROOT_DIR/cmd/actrail-importer" \
  -source-sqlite "$SNAPSHOT_DIR/sqlite.db" \
  -target-sqlite "$TARGET_DB" \
  -side-dir "$SNAPSHOT_DIR" \
  -report-json "$REPORT_PATH"

printf 'work_dir: %s\n' "$WORK_DIR"
printf 'snapshot_sqlite: %s\n' "$SNAPSHOT_DIR/sqlite.db"
printf 'target_sqlite: %s\n' "$TARGET_DB"
printf 'report_json: %s\n' "$REPORT_PATH"
