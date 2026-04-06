#!/bin/sh
set -eu

cfg_file="${HAPROXY_CFG_PATH:-/tmp/haproxy.cfg}"
redis_master_host="${REDIS_MASTER_HOST:-redis-1}"
redis_master_port="${REDIS_MASTER_PORT_NUMBER:-6379}"
redis_sentinel_host="${REDIS_SENTINEL_HOST:-redis-sentinel}"
redis_sentinel_port="${REDIS_SENTINEL_PORT_NUMBER:-26379}"
redis_master_set="${REDIS_MASTER_SET:-${REDIS_SENTINEL_MASTER_SET:-mymaster}}"
redis_password="${REDIS_PASSWORD:-}"
sentinel_lookup_retries="${REDIS_SENTINEL_LOOKUP_RETRIES:-60}"
sentinel_lookup_interval="${REDIS_SENTINEL_LOOKUP_INTERVAL:-2}"

resp_command() {
  printf '*%s\r\n' "$#"
  for arg in "$@"; do
    len="$(printf '%s' "$arg" | wc -c | tr -d '[:space:]')"
    printf '$%s\r\n%s\r\n' "$len" "$arg"
  done
}

probe_redis_endpoint() {
  probe_host="$1"
  probe_port="$2"

  if [ -n "$redis_password" ]; then
    response="$(
      {
        resp_command AUTH "$redis_password"
        resp_command PING
      } \
        | nc -w 3 "$probe_host" "$probe_port" 2>/dev/null \
        | tr '\r' '\n'
    )"
    printf '%s\n' "$response" | grep -q '^+OK$' || return 1
  else
    response="$(
      resp_command PING \
        | nc -w 3 "$probe_host" "$probe_port" 2>/dev/null \
        | tr '\r' '\n'
    )"
  fi

  printf '%s\n' "$response" | grep -q '^+PONG$'
}

discover_master_from_sentinel() {
  attempt=1
  while [ "$attempt" -le "$sentinel_lookup_retries" ]; do
    response="$(
      resp_command SENTINEL get-master-addr-by-name "$redis_master_set" \
        | nc -w 3 "$redis_sentinel_host" "$redis_sentinel_port" 2>/dev/null \
        | tr '\r' '\n' \
        | awk 'NF { print }'
    )"
    discovered_host="$(printf '%s\n' "$response" | sed -n '3p')"
    discovered_port="$(printf '%s\n' "$response" | sed -n '5p')"

    if [ -n "${discovered_host:-}" ] && [ -n "${discovered_port:-}" ]; then
      if probe_redis_endpoint "$discovered_host" "$discovered_port"; then
        redis_master_host="$discovered_host"
        redis_master_port="$discovered_port"
        echo "[redis-proxy-bootstrap] 使用 Sentinel 发现并验证主库: ${redis_master_host}:${redis_master_port}" >&2
        return 0
      fi

      echo "[redis-proxy-bootstrap] Sentinel 返回 ${discovered_host}:${discovered_port}，但当前不可直连，继续重试。" >&2
    fi

    echo "[redis-proxy-bootstrap] 第 ${attempt} 次从 Sentinel 获取主库失败，${sentinel_lookup_interval}s 后重试。" >&2
    sleep "$sentinel_lookup_interval"
    attempt=$((attempt + 1))
  done

  echo "[redis-proxy-bootstrap] Sentinel 未返回主库，回退到 REDIS_MASTER_HOST=${redis_master_host}:${redis_master_port}" >&2
  return 1
}

discover_master_from_sentinel || true

cat >"$cfg_file" <<EOF
global
  log stdout format raw local0

defaults
  log global
  mode tcp
  option tcplog
  timeout connect 5s
  timeout check 5s
  timeout client 1m
  timeout server 1m

frontend redis_front
  bind *:6379
  default_backend redis_master

backend redis_master
  server redis_primary ${redis_master_host}:${redis_master_port} check inter 2s fall 3 rise 2 on-marked-down shutdown-sessions
EOF

exec haproxy -W -db -f "$cfg_file"
