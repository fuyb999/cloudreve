#!/bin/sh
set -eu

cfg_file="${HAPROXY_CFG_PATH:-/tmp/haproxy.cfg}"

cat >"$cfg_file" <<EOF
global
  log stdout format raw local0

defaults
  log global
  mode tcp
  option tcplog
  timeout connect 5s
  timeout client 1m
  timeout server 1m

frontend redis_front
  bind *:6379
  default_backend redis_master

backend redis_master
  option tcp-check
  tcp-check connect
  tcp-check send AUTH\ ${REDIS_PASSWORD}\r\n
  tcp-check expect string +OK
  tcp-check send INFO\ REPLICATION\r\n
  tcp-check expect string role:master
  tcp-check send QUIT\r\n
  tcp-check expect string +OK
  server redis1 redis-1:6379 check inter 2s fall 3 rise 2 on-marked-down shutdown-sessions
  server redis2 redis-2:6379 check inter 2s fall 3 rise 2 on-marked-down shutdown-sessions
  server redis3 redis-3:6379 check inter 2s fall 3 rise 2 on-marked-down shutdown-sessions
EOF

exec haproxy -W -db -f "$cfg_file"
