#!/bin/sh
set -eu

cfg_file="${HAPROXY_CFG_PATH:-/tmp/haproxy.cfg}"
redis_master_host="${REDIS_MASTER_HOST:-redis-1}"
redis_master_port="${REDIS_MASTER_PORT_NUMBER:-6379}"
redis_nodes="${REDIS_NODES:-redis-1:6379,redis-2:6379,redis-3:6379}"
redis_sentinel_host="${REDIS_SENTINEL_HOST:-redis-sentinel}"
redis_sentinel_port="${REDIS_SENTINEL_PORT_NUMBER:-26379}"
redis_master_set="${REDIS_MASTER_SET:-${REDIS_SENTINEL_MASTER_SET:-mymaster}}"
redis_sentinel_enabled="${REDIS_SENTINEL_ENABLED:-yes}"
redis_password="${REDIS_PASSWORD:-}"
sentinel_lookup_retries="${REDIS_SENTINEL_LOOKUP_RETRIES:-60}"
sentinel_lookup_interval="${REDIS_SENTINEL_LOOKUP_INTERVAL:-2}"
haproxy_connect_timeout="${REDIS_HAPROXY_CONNECT_TIMEOUT:-5s}"
haproxy_check_timeout="${REDIS_HAPROXY_CHECK_TIMEOUT:-5s}"
haproxy_client_timeout="${REDIS_HAPROXY_CLIENT_TIMEOUT:-1m}"
haproxy_server_timeout="${REDIS_HAPROXY_SERVER_TIMEOUT:-1m}"
haproxy_check_interval="${REDIS_HAPROXY_CHECK_INTERVAL:-2s}"
haproxy_check_fall="${REDIS_HAPROXY_CHECK_FALL:-3}"
haproxy_check_rise="${REDIS_HAPROXY_CHECK_RISE:-2}"
haproxy_maxconn="${REDIS_HAPROXY_MAXCONN:-4096}"

resp_command() {
  printf '*%s\r\n' "$#"
  for arg in "$@"; do
    len="$(printf '%s' "$arg" | wc -c | tr -d '[:space:]')"
    printf '$%s\r\n%s\r\n' "$len" "$arg"
  done
}

resp_command_hex() {
  resp_command "$@" | od -An -tx1 -v | tr -d '[:space:]'
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

if [ -n "$(printf '%s' "$redis_nodes" | tr -d '[:space:]')" ]; then
  echo "[redis-proxy-bootstrap] 使用 REDIS_NODES 动态主库健康检查，跳过启动期 Sentinel 阻塞探测。" >&2
else
  case "$redis_sentinel_enabled" in
    yes|true|1|on)
      discover_master_from_sentinel || true
      ;;
    *)
      echo "[redis-proxy-bootstrap] 已关闭 Sentinel 探测，直接使用 REDIS_MASTER_HOST=${redis_master_host}:${redis_master_port}" >&2
      ;;
  esac
fi

cat >"$cfg_file" <<EOF
global
  log stdout format raw local0
  maxconn ${haproxy_maxconn}

defaults
  log global
  mode tcp
  option tcplog
  timeout connect ${haproxy_connect_timeout}
  timeout check ${haproxy_check_timeout}
  timeout client ${haproxy_client_timeout}
  timeout server ${haproxy_server_timeout}

frontend redis_front
  bind *:6379
  default_backend redis_master

backend redis_master
  option tcp-check
EOF

if [ -n "$redis_password" ]; then
  {
    printf '  tcp-check send-binary %s\n' "$(resp_command_hex AUTH "$redis_password")"
    printf '  tcp-check expect string +OK\n'
  } >>"$cfg_file"
fi

cat >>"$cfg_file" <<EOF
  tcp-check send-binary $(resp_command_hex PING)
  tcp-check expect string +PONG
  tcp-check send-binary $(resp_command_hex INFO replication)
  tcp-check expect string role:master
  tcp-check send-binary $(resp_command_hex QUIT)
EOF

node_index=1
old_ifs="$IFS"
IFS=','
for node in $redis_nodes; do
  IFS="$old_ifs"
  node="$(printf '%s' "$node" | tr -d '[:space:]')"
  if [ -n "$node" ]; then
    printf '  server redis%s %s check inter %s fall %s rise %s on-marked-down shutdown-sessions\n' \
      "$node_index" \
      "$node" \
      "$haproxy_check_interval" \
      "$haproxy_check_fall" \
      "$haproxy_check_rise" \
      >>"$cfg_file"
    node_index=$((node_index + 1))
  fi
  IFS=','
done
IFS="$old_ifs"

if [ "$node_index" -eq 1 ]; then
  printf '  server redis_primary %s:%s check inter %s fall %s rise %s on-marked-down shutdown-sessions\n' \
    "$redis_master_host" \
    "$redis_master_port" \
    "$haproxy_check_interval" \
    "$haproxy_check_fall" \
    "$haproxy_check_rise" \
    >>"$cfg_file"
fi

if [ "${REDIS_PROXY_TEST_RENDER_ONLY:-0}" = "1" ]; then
  exit 0
fi

exec haproxy -W -db -f "$cfg_file"
