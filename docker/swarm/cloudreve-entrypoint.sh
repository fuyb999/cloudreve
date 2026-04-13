#!/bin/sh

set -eu

CLOUDREVE_WAIT_FOR_TIMEOUT="${CLOUDREVE_WAIT_FOR_TIMEOUT:-180}"
CLOUDREVE_WAIT_FOR_INTERVAL="${CLOUDREVE_WAIT_FOR_INTERVAL:-2}"

parse_host_port() {
  value="$1"
  default_port="$2"

  value="${value#*://}"
  value="${value%%/*}"

  case "$value" in
    *:*)
      host="${value%:*}"
      port="${value##*:}"
      ;;
    *)
      host="$value"
      port="$default_port"
      ;;
  esac

  printf '%s %s\n' "$host" "$port"
}

wait_for_endpoint() {
  label="$1"
  host="$2"
  port="$3"
  deadline=$(( $(date +%s) + CLOUDREVE_WAIT_FOR_TIMEOUT ))

  while ! nc -z "$host" "$port" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "[cloudreve-entrypoint] 等待 ${label} 超时: ${host}:${port}" >&2
      exit 1
    fi

    echo "[cloudreve-entrypoint] 等待 ${label}: ${host}:${port}"
    sleep "$CLOUDREVE_WAIT_FOR_INTERVAL"
  done

  echo "[cloudreve-entrypoint] ${label} 已就绪: ${host}:${port}"
}

db_host="$(printenv 'CR_CONF_Database.Host' || true)"
db_port="$(printenv 'CR_CONF_Database.Port' || true)"
redis_server="$(printenv 'CR_CONF_Redis.Server' || true)"
storage_type="$(printenv 'CR_INIT_DEFAULT_STORAGE' || true)"
s3_endpoint="$(printenv 'CR_INIT_S3_ENDPOINT' || true)"

if [ -n "$db_host" ]; then
  wait_for_endpoint "PostgreSQL" "$db_host" "${db_port:-5432}"
fi

if [ -n "$redis_server" ]; then
  set -- $(parse_host_port "$redis_server" "6379")
  wait_for_endpoint "Redis" "$1" "$2"
fi

if [ "$storage_type" = "s3" ] && [ -n "$s3_endpoint" ]; then
  set -- $(parse_host_port "$s3_endpoint" "443")
  wait_for_endpoint "S3" "$1" "$2"
fi

exec sh ./entrypoint.sh "$@"
