#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
SCRIPT="$ROOT_DIR/docker/swarm/redis-proxy-bootstrap.sh"

assert_contains() {
  file="$1"
  expected="$2"
  if ! grep -Fq "$expected" "$file"; then
    echo "expected to find: $expected" >&2
    echo "--- $file ---" >&2
    cat "$file" >&2
    exit 1
  fi
}

assert_not_contains() {
  file="$1"
  unexpected="$2"
  if grep -Fq "$unexpected" "$file"; then
    echo "expected not to find: $unexpected" >&2
    echo "--- $file ---" >&2
    cat "$file" >&2
    exit 1
  fi
}

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/redis-proxy-bootstrap-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

cfg_file="$tmp_dir/haproxy.cfg"

HAPROXY_CFG_PATH="$cfg_file" \
REDIS_SENTINEL_ENABLED=no \
REDIS_NODES=redis-a:6380,redis-b:6381,redis-c:6382 \
REDIS_MASTER_HOST=redis-a \
REDIS_MASTER_PORT_NUMBER=6380 \
REDIS_PASSWORD="pa ss" \
REDIS_HAPROXY_CONNECT_TIMEOUT=7s \
REDIS_HAPROXY_CLIENT_TIMEOUT=2m \
REDIS_HAPROXY_SERVER_TIMEOUT=2m \
REDIS_HAPROXY_CHECK_INTERVAL=3s \
REDIS_HAPROXY_CHECK_FALL=5 \
REDIS_HAPROXY_CHECK_RISE=4 \
REDIS_HAPROXY_MAXCONN=20000 \
REDIS_PROXY_TEST_RENDER_ONLY=1 \
sh "$SCRIPT" >/dev/null

assert_contains "$cfg_file" "maxconn 20000"
assert_contains "$cfg_file" "timeout connect 7s"
assert_contains "$cfg_file" "timeout client 2m"
assert_contains "$cfg_file" "timeout server 2m"
assert_contains "$cfg_file" "resolvers docker"
assert_contains "$cfg_file" "nameserver dns 127.0.0.11:53"
assert_contains "$cfg_file" "default-server resolvers docker init-addr libc,none resolve-prefer ipv4"
assert_contains "$cfg_file" "tcp-check connect"
assert_contains "$cfg_file" "tcp-check send-binary 2a320d0a24340d0a415554480d0a24350d0a70612073730d0a"
assert_contains "$cfg_file" "tcp-check send-binary 2a310d0a24340d0a50494e470d0a"
assert_contains "$cfg_file" "tcp-check send-binary 2a320d0a24340d0a494e464f0d0a2431310d0a7265706c69636174696f6e0d0a"
assert_contains "$cfg_file" "tcp-check expect string role:master"
assert_not_contains "$cfg_file" "tcp-check send AUTH"
assert_contains "$cfg_file" "server redis1 redis-a:6380 check inter 3s fall 5 rise 4 on-marked-down shutdown-sessions"
assert_contains "$cfg_file" "server redis2 redis-b:6381 check inter 3s fall 5 rise 4 on-marked-down shutdown-sessions"
assert_contains "$cfg_file" "server redis3 redis-c:6382 check inter 3s fall 5 rise 4 on-marked-down shutdown-sessions"

startup_log="$tmp_dir/startup.log"
REDIS_SENTINEL_HOST=127.0.0.1 \
REDIS_SENTINEL_PORT_NUMBER=1 \
REDIS_SENTINEL_ENABLED=yes \
REDIS_SENTINEL_LOOKUP_RETRIES=1 \
REDIS_SENTINEL_LOOKUP_INTERVAL=0 \
REDIS_NODES=redis-a:6380,redis-b:6381 \
REDIS_PROXY_TEST_RENDER_ONLY=1 \
HAPROXY_CFG_PATH="$tmp_dir/haproxy-sentinel-skip.cfg" \
sh "$SCRIPT" >/dev/null 2>"$startup_log"

assert_contains "$startup_log" "使用 REDIS_NODES 动态主库健康检查，跳过启动期 Sentinel 阻塞探测"

echo "redis-proxy-bootstrap-test: ok"
