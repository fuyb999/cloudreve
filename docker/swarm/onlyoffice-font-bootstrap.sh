#!/usr/bin/env bash

set -euo pipefail

ONLYOFFICE_CUSTOM_FONTS_DIR="${ONLYOFFICE_CUSTOM_FONTS_DIR:-/usr/share/fonts/truetype/custom}"
ONLYOFFICE_DB_READY_TIMEOUT_SECONDS="${ONLYOFFICE_DB_READY_TIMEOUT_SECONDS:-300}"
ONLYOFFICE_DB_READY_INTERVAL_SECONDS="${ONLYOFFICE_DB_READY_INTERVAL_SECONDS:-2}"

wait_for_postgres_sql_ready() {
  local db_type="${DB_TYPE:-postgres}"
  local db_host="${DB_HOST:-}"
  local db_port="${DB_PORT:-5432}"
  local db_name="${DB_NAME:-}"
  local db_user="${DB_USER:-}"
  local db_password="${DB_PWD:-}"
  local elapsed=0

  case "$db_type" in
    ""|postgres)
      ;;
    *)
      return 0
      ;;
  esac

  if [[ -z "$db_host" || "$db_host" == "localhost" ]]; then
    return 0
  fi
  if [[ -z "$db_name" || -z "$db_user" ]]; then
    return 0
  fi
  if ! command -v psql >/dev/null 2>&1; then
    echo "[onlyoffice-font-bootstrap] 缺少 psql，跳过 PostgreSQL SQL 就绪等待。" >&2
    return 0
  fi

  export PGPASSWORD="$db_password"

  until psql \
    -h "$db_host" \
    -p "$db_port" \
    -U "$db_user" \
    -d "$db_name" \
    -w \
    -v ON_ERROR_STOP=1 \
    -tAc 'select 1' >/dev/null 2>&1; do
    if (( elapsed >= ONLYOFFICE_DB_READY_TIMEOUT_SECONDS )); then
      echo "[onlyoffice-font-bootstrap] PostgreSQL SQL 预检超时: ${db_host}:${db_port}/${db_name}" >&2
      return 1
    fi
    echo "[onlyoffice-font-bootstrap] 等待 PostgreSQL SQL 可执行: ${db_host}:${db_port}/${db_name}" >&2
    sleep "$ONLYOFFICE_DB_READY_INTERVAL_SECONDS"
    elapsed=$((elapsed + ONLYOFFICE_DB_READY_INTERVAL_SECONDS))
  done
}

if [[ -d "$ONLYOFFICE_CUSTOM_FONTS_DIR" ]]; then
  fc-cache -f "$ONLYOFFICE_CUSTOM_FONTS_DIR" >/dev/null 2>&1 || fc-cache -f >/dev/null 2>&1 || true
  documentserver-generate-allfonts.sh >/dev/null 2>&1 || true
fi

wait_for_postgres_sql_ready

exec /app/ds/run-document-server.sh "$@"
