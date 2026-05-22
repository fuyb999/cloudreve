#!/bin/sh
set -eu

host="${REDIS_PROXY_HEALTH_HOST:-127.0.0.1}"
port="${REDIS_PROXY_HEALTH_PORT:-6379}"
require_master="${REDIS_PROXY_HEALTH_REQUIRE_MASTER:-yes}"
redis_password="${REDIS_PASSWORD:-}"

if [ -z "$redis_password" ] && [ -f /run/secrets/redis_password ]; then
  redis_password="$(cat /run/secrets/redis_password)"
fi

resp_command() {
  printf '*%s\r\n' "$#"
  for arg in "$@"; do
    len="$(printf '%s' "$arg" | wc -c | tr -d '[:space:]')"
    printf '$%s\r\n%s\r\n' "$len" "$arg"
  done
}

{
  if [ -n "$redis_password" ]; then
    resp_command AUTH "$redis_password"
  fi
  resp_command PING
  if [ "$require_master" = "yes" ] || [ "$require_master" = "true" ] || [ "$require_master" = "1" ]; then
    resp_command INFO replication
  fi
  resp_command QUIT
} | nc -w "${REDIS_PROXY_HEALTH_TIMEOUT_SECONDS:-4}" "$host" "$port" \
  | tr '\r' '\n' \
  | awk -v require_master="$require_master" '
      /^\+OK$/ { auth_ok = 1 }
      /^\+PONG$/ { pong_ok = 1 }
      /^role:master$/ { master_ok = 1 }
      END {
        if (!pong_ok) exit 1
        if ((require_master == "yes" || require_master == "true" || require_master == "1") && !master_ok) exit 1
      }
    '
