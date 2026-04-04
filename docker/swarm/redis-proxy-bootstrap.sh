#!/bin/sh
set -eu

: "${REDIS_PASSWORD:?set REDIS_PASSWORD}"

cfg_file="${HAPROXY_CFG_PATH:-/tmp/haproxy.cfg}"
redis_master_host="${REDIS_MASTER_HOST:-redis-1}"
redis_master_port="${REDIS_MASTER_PORT_NUMBER:-6379}"

resp_to_hex() {
  {
    printf '*%s\r\n' "$#"
    for arg in "$@"; do
      len="$(printf '%s' "$arg" | wc -c | tr -d '[:space:]')"
      printf '$%s\r\n%s\r\n' "$len" "$arg"
    done
  } | xxd -p -c 256 | tr -d '\n'
}

auth_cmd_hex="$(resp_to_hex AUTH "$REDIS_PASSWORD")"
ping_cmd_hex="$(resp_to_hex PING)"
quit_cmd_hex="$(resp_to_hex QUIT)"

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
  option tcp-check
  tcp-check connect
  tcp-check send-binary ${auth_cmd_hex}
  tcp-check expect string +OK
  tcp-check send-binary ${ping_cmd_hex}
  tcp-check expect string +PONG
  tcp-check send-binary ${quit_cmd_hex}
  tcp-check expect string +OK
  server redis_primary ${redis_master_host}:${redis_master_port} check inter 2s fall 3 rise 2 on-marked-down shutdown-sessions
EOF

exec haproxy -W -db -f "$cfg_file"
