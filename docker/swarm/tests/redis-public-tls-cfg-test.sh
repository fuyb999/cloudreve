#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
CFG="$ROOT_DIR/docker/swarm/haproxy/redis-public-tls.cfg"

assert_contains() {
  expected="$1"
  if ! grep -Fq "$expected" "$CFG"; then
    echo "expected to find: $expected" >&2
    echo "--- $CFG ---" >&2
    cat "$CFG" >&2
    exit 1
  fi
}

assert_not_contains() {
  unexpected="$1"
  if grep -Fq "$unexpected" "$CFG"; then
    echo "expected not to find: $unexpected" >&2
    echo "--- $CFG ---" >&2
    cat "$CFG" >&2
    exit 1
  fi
}

assert_contains "tcp-check send-binary 2a310d0a24340d0a50494e470d0a"
assert_contains "tcp-check expect rstring ^(\\+PONG|-NOAUTH)"
assert_contains "server-template redis 10 tasks.redis-proxy-internal:6379 check inter 2s fall 3 rise 2"
assert_not_contains "server redis redis-proxy-internal:6379 check"

echo "redis-public-tls-cfg-test: ok"
