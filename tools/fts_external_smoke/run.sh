#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DB_PATH="${CR_CONF_Database_DBFile:-$ROOT_DIR/.tmp/data/cloudreve.db}"

clear_cache() {
  if command -v docker >/dev/null 2>&1; then
    docker exec redis redis-cli --scan --pattern 'setting_fts_external*' | while read -r key; do
      docker exec redis redis-cli del "$key" >/dev/null
    done || true
  fi
}

set_timeout() {
  local timeout="$1"
  sqlite3 "$DB_PATH" "update settings set value='${timeout}' where name='fts_external_timeout_seconds';"
  clear_cache
}

run_tool() {
  (
    cd "$ROOT_DIR"
    env \
      CR_CONF_Database.Type=sqlite \
      CR_CONF_Database.DBFile="$DB_PATH" \
      "$@" \
      go run ./tools/fts_external_smoke
  )
}

usage() {
  cat <<'EOF'
用法：
  ./tools/fts_external_smoke/run.sh success [count]
  ./tools/fts_external_smoke/run.sh fallback [count] [delay_seconds]
  ./tools/fts_external_smoke/run.sh cleanup-prefix <prefix>
  ./tools/fts_external_smoke/run.sh cleanup-ids <id1,id2,...>
EOF
}

cmd="${1:-}"
if [[ -z "$cmd" ]]; then
  usage
  exit 1
fi
shift || true

case "$cmd" in
  success)
    count="${1:-1}"
    set_timeout 300
    run_tool REAL_FTS_SMOKE_BATCH_COUNT="$count"
    ;;
  fallback)
    count="${1:-2}"
    delay="${2:-12}"
    set_timeout 1
    trap 'set_timeout 300' EXIT
    run_tool REAL_FTS_SMOKE_BATCH_COUNT="$count" REAL_FTS_SMOKE_RESULT_DELAY_SECONDS="$delay"
    set_timeout 300
    trap - EXIT
    ;;
  cleanup-prefix)
    prefix="${1:-}"
    if [[ -z "$prefix" ]]; then
      echo "缺少 prefix"
      exit 1
    fi
    run_tool REAL_FTS_SMOKE_CLEANUP_PREFIX="$prefix"
    ;;
  cleanup-ids)
    ids="${1:-}"
    if [[ -z "$ids" ]]; then
      echo "缺少 id 列表"
      exit 1
    fi
    run_tool REAL_FTS_SMOKE_CLEANUP_FILE_IDS="$ids"
    ;;
  *)
    usage
    exit 1
    ;;
esac
